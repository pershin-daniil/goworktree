package inspectworks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

const systemInspectionParallelism = 8

type WorkInspector interface {
	Inspect(context.Context, inspectwork.Request) (inspectwork.Snapshot, error)
}

type OperationReader interface {
	Load(string) (newwork.OperationRecord, error)
}

type Inspector struct {
	Works       WorkInspector
	Operations  OperationReader
	Now         func() time.Time
	Parallelism int
}

func NewSystemInspector() Inspector {
	limiter := inspectwork.NewRepositoryLimiter(systemInspectionParallelism)
	return Inspector{
		Works: inspectwork.Inspector{
			Git:        inspectwork.SystemGit{},
			Operations: inspectwork.SystemOperationReader{},
			Limiter:    limiter,
		},
		Operations:  inspectwork.SystemOperationReader{},
		Parallelism: systemInspectionParallelism,
	}
}

func (i Inspector) Inspect(ctx context.Context, request Request) (Snapshot, error) {
	if i.Works == nil {
		return Snapshot{}, fmt.Errorf("Inspect Works Work inspector is nil")
	}
	if i.Operations == nil {
		return Snapshot{}, fmt.Errorf("Inspect Works operation reader is nil")
	}
	if request.WorksRoot == "" {
		return Snapshot{}, fmt.Errorf("works root is empty")
	}
	if request.ControlRoot == "" {
		return Snapshot{}, fmt.Errorf("control root is empty")
	}
	worksRoot, worksRootExists, err := canonicalOptionalDirectory(request.WorksRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect works root: %w", err)
	}
	controlRoot, _, err := canonicalOptionalDirectory(request.ControlRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect control root: %w", err)
	}
	if pathsOverlap(worksRoot, controlRoot) {
		return Snapshot{}, fmt.Errorf("control root and works root must not contain each other")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	snapshot := Snapshot{
		InspectedAt: i.now().UTC(),
		WorksRoot:   worksRoot,
		ControlRoot: controlRoot,
	}
	candidates := make(map[string]*Work)
	if worksRootExists {
		i.discoverDirectories(&snapshot, candidates)
	}
	i.discoverOperations(&snapshot, candidates)

	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	works, err := i.inspectCandidates(ctx, worksRoot, controlRoot, names, candidates)
	snapshot.Works = works
	if err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (i Inspector) inspectCandidates(
	ctx context.Context,
	worksRoot string,
	controlRoot string,
	names []string,
	candidates map[string]*Work,
) ([]Work, error) {
	results := make([]Work, len(names))
	for index, name := range names {
		results[index] = *candidates[name]
	}

	limit := make(chan struct{}, max(1, i.Parallelism))
	var inspections sync.WaitGroup
	var contextErr error
	for index, name := range names {
		if _, parseErr := work.ParseName(name); parseErr != nil {
			continue
		}
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			contextErr = ctx.Err()
		}
		if contextErr != nil {
			break
		}
		inspections.Add(1)
		go func() {
			defer inspections.Done()
			defer func() { <-limit }()
			entry := results[index]
			observed, inspectErr := i.Works.Inspect(ctx, inspectwork.Request{
				WorksRoot: worksRoot, ControlRoot: controlRoot, Name: name,
			})
			if inspectErr != nil {
				entry.Problems = append(entry.Problems, Problem{
					Code: ProblemWorkInspectionFailed, Name: name, Path: entry.RootPath,
					Message: inspectErr.Error(),
				})
			} else {
				entry.Snapshot = &observed
			}
			results[index] = entry
		}()
	}
	inspections.Wait()
	if contextErr == nil {
		contextErr = ctx.Err()
	}
	return results, contextErr
}

func (i Inspector) discoverDirectories(snapshot *Snapshot, candidates map[string]*Work) {
	entries, err := os.ReadDir(snapshot.WorksRoot)
	if err != nil {
		snapshot.Problems = append(snapshot.Problems, Problem{
			Code: ProblemWorksRootUnreadable, Path: snapshot.WorksRoot, Message: err.Error(),
		})
		return
	}
	for _, directoryEntry := range entries {
		if !directoryEntry.IsDir() && directoryEntry.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := directoryEntry.Name()
		path := filepath.Join(snapshot.WorksRoot, name)
		entry := candidate(candidates, name, path)
		entry.FromDirectory = true
		if _, err := work.ParseName(name); err != nil {
			entry.Problems = append(entry.Problems, Problem{
				Code: ProblemEntryNameInvalid, Name: name, Path: path, Message: err.Error(),
			})
		}
	}
}

func (i Inspector) discoverOperations(snapshot *Snapshot, candidates map[string]*Work) {
	directory := filepath.Join(snapshot.ControlRoot, "operations", "new-work")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		snapshot.Problems = append(snapshot.Problems, Problem{
			Code: ProblemOperationDirectoryUnread, Path: directory, Message: err.Error(),
		})
		return
	}
	for _, operationEntry := range entries {
		path := filepath.Join(directory, operationEntry.Name())
		info, infoErr := operationEntry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || !strings.HasSuffix(operationEntry.Name(), ".json") {
			message := "operation entry is not a regular JSON file"
			if infoErr != nil {
				message = infoErr.Error()
			}
			snapshot.Problems = append(snapshot.Problems, Problem{
				Code: ProblemOperationEntryUnsafe, Path: path, Message: message,
			})
			continue
		}
		record, loadErr := i.Operations.Load(path)
		if loadErr != nil {
			snapshot.Problems = append(snapshot.Problems, Problem{
				Code: ProblemOperationRecordInvalid, Path: path, Message: loadErr.Error(),
			})
			continue
		}
		name := record.Plan.WorkName.String()
		if _, parseErr := work.ParseName(name); parseErr != nil {
			snapshot.Problems = append(snapshot.Problems, Problem{
				Code: ProblemOperationRecordInvalid, Name: name, Path: path, Message: parseErr.Error(),
			})
			continue
		}
		recordWorksRoot, _, rootErr := canonicalOptionalDirectory(filepath.Dir(filepath.Clean(record.Plan.WorkRoot)))
		if rootErr != nil || recordWorksRoot != snapshot.WorksRoot {
			snapshot.Problems = append(snapshot.Problems, Problem{
				Code: ProblemOperationOutsideWorksRoot, Name: name, Path: path,
				Message: fmt.Sprintf("operation Work root %s is outside configured works root", record.Plan.WorkRoot),
			})
			continue
		}
		entry := candidate(candidates, name, filepath.Join(snapshot.WorksRoot, name))
		entry.FromOperation = true
	}
}

func candidate(candidates map[string]*Work, name, root string) *Work {
	if existing, ok := candidates[name]; ok {
		return existing
	}
	entry := &Work{Name: name, RootPath: root}
	candidates[name] = entry
	return entry
}

func canonicalOptionalDirectory(path string) (string, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(abs), false, nil
	}
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("not a directory: %s", canonical)
	}
	return filepath.Clean(canonical), true, nil
}

func pathsOverlap(a, b string) bool {
	return within(a, b) || within(b, a)
}

func within(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (i Inspector) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}
