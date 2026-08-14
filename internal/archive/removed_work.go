package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
)

const SchemaVersion = 1

var archiveIDPattern = regexp.MustCompile(`^.+-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	ArchiveID     string    `json:"archive_id"`
	WorkName      string    `json:"work_name"`
	WorkID        string    `json:"work_id"`
	RemovalID     string    `json:"removal_id"`
	CreatedAt     time.Time `json:"created_at"`
	Files         []File    `json:"files"`
}

type Summary struct {
	Manifest Manifest
	Path     string
}

type PublishRequest struct {
	ArchiveID        string
	WorkName         string
	WorkID           string
	RemovalID        string
	CreatedAt        time.Time
	NewWorkRecord    string
	SyncWorkRecord   string
	RemoveWorkRecord string
}

type Store struct {
	ControlRoot string
	Now         func() time.Time
	Rand        func([]byte) (int, error)
	Locks       lockops.Set
}

func (s Store) Root() string {
	return filepath.Join(s.ControlRoot, "archives", "removed-work")
}

func (s Store) NewID(workName string) (string, error) {
	if _, err := work.ParseName(workName); err != nil {
		return "", err
	}
	if err := s.ensureRoot(); err != nil {
		return "", err
	}
	read := s.Rand
	if read == nil {
		read = rand.Read
	}
	stamp := s.now().UTC().Format("20060102T150405Z")
	for attempt := 0; attempt < 16; attempt++ {
		salt := make([]byte, 4)
		n, err := read(salt)
		if err != nil {
			return "", fmt.Errorf("generate archive salt: %w", err)
		}
		if n != len(salt) {
			return "", fmt.Errorf("archive random source returned %d bytes; want %d", n, len(salt))
		}
		id := workName + "-" + stamp + "-" + hex.EncodeToString(salt)
		if _, err := os.Lstat(filepath.Join(s.Root(), id)); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(filepath.Join(s.Root(), ".deleting-"+id)); errors.Is(err, os.ErrNotExist) {
				return id, nil
			} else if err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate a unique archive ID after 16 attempts")
}

func (s Store) Publish(request PublishRequest) (Summary, error) {
	if err := validateArchiveID(request.ArchiveID); err != nil {
		return Summary{}, err
	}
	if _, err := work.ParseName(request.WorkName); err != nil {
		return Summary{}, fmt.Errorf("invalid archived Work name: %w", err)
	}
	if !strings.HasPrefix(request.ArchiveID, request.WorkName+"-") {
		return Summary{}, fmt.Errorf("archive ID does not belong to Work %q", request.WorkName)
	}
	if request.WorkID == "" || request.RemovalID == "" || request.RemoveWorkRecord == "" || request.NewWorkRecord == "" {
		return Summary{}, fmt.Errorf("archive publication request is incomplete")
	}
	if err := s.ensureRoot(); err != nil {
		return Summary{}, err
	}
	final := filepath.Join(s.Root(), request.ArchiveID)
	if _, err := os.Lstat(final); err == nil {
		summary, err := s.Show(request.ArchiveID)
		if err != nil {
			return Summary{}, fmt.Errorf("validate published archive: %w", err)
		}
		if summary.Manifest.WorkID != request.WorkID || summary.Manifest.RemovalID != request.RemovalID {
			return Summary{}, fmt.Errorf("archive ID %q belongs to a different removal", request.ArchiveID)
		}
		return summary, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Summary{}, err
	}

	stage, err := os.MkdirTemp(s.Root(), ".staging-"+request.ArchiveID+"-")
	if err != nil {
		return Summary{}, fmt.Errorf("create archive staging directory: %w", err)
	}
	staged := false
	defer func() {
		if !staged {
			_ = os.RemoveAll(stage)
		}
	}()

	sources := []struct {
		name, path string
		optional   bool
	}{
		{name: "new-work.json", path: request.NewWorkRecord},
		{name: "sync-work.json", path: request.SyncWorkRecord, optional: true},
		{name: "remove-work.json", path: request.RemoveWorkRecord},
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion, ArchiveID: request.ArchiveID, WorkName: request.WorkName,
		WorkID: request.WorkID, RemovalID: request.RemovalID, CreatedAt: request.CreatedAt.UTC(),
	}
	for _, source := range sources {
		if source.path == "" {
			if source.optional {
				continue
			}
			return Summary{}, fmt.Errorf("archive source %s is empty", source.name)
		}
		data, err := work.ReadRegularFile(source.path)
		if errors.Is(err, os.ErrNotExist) && source.optional {
			continue
		}
		if err != nil {
			return Summary{}, fmt.Errorf("read archive source %s: %w", source.name, err)
		}
		if err := work.CreateFile(filepath.Join(stage, source.name), data, 0o600); err != nil {
			return Summary{}, fmt.Errorf("stage archive source %s: %w", source.name, err)
		}
		digest := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, File{Name: source.name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))})
	}
	if len(manifest.Files) < 2 {
		return Summary{}, fmt.Errorf("archive must contain New Work and Remove Work records")
	}
	if err := work.CreateJSON(filepath.Join(stage, "archive.json"), manifest, 0o600); err != nil {
		return Summary{}, fmt.Errorf("write archive manifest: %w", err)
	}
	if err := work.SyncDirectory(stage); err != nil {
		return Summary{}, fmt.Errorf("sync archive staging directory: %w", err)
	}
	if err := os.Rename(stage, final); err != nil {
		return Summary{}, fmt.Errorf("publish archive: %w", err)
	}
	staged = true
	if err := work.SyncDirectory(s.Root()); err != nil {
		return Summary{}, fmt.Errorf("sync archive root: %w", err)
	}
	return Summary{Manifest: manifest, Path: final}, nil
}

func (s Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.Root())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Summary, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		summary, err := s.Show(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("inspect archive %s: %w", entry.Name(), err)
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Manifest.CreatedAt.Equal(result[j].Manifest.CreatedAt) {
			return result[i].Manifest.ArchiveID < result[j].Manifest.ArchiveID
		}
		return result[i].Manifest.CreatedAt.After(result[j].Manifest.CreatedAt)
	})
	return result, nil
}

func (s Store) Show(id string) (Summary, error) {
	if err := validateArchiveID(id); err != nil {
		return Summary{}, err
	}
	path := filepath.Join(s.Root(), id)
	manifest, err := validateDirectory(path, id)
	if err != nil {
		return Summary{}, err
	}
	return Summary{Manifest: manifest, Path: path}, nil
}

func (s Store) Delete(ctx context.Context, id, confirmation string) error {
	if err := validateArchiveID(id); err != nil {
		return err
	}
	if confirmation != id {
		return fmt.Errorf("confirmation must exactly equal archive ID %q", id)
	}
	locks := s.Locks
	if locks.Root == "" {
		locks.Root = s.ControlRoot
	}
	lease, err := locks.Acquire(ctx, "archives", []string{id})
	if err != nil {
		return fmt.Errorf("acquire archive lock: %w", err)
	}
	defer func() { _ = lease.Release() }()
	if err := s.ensureRoot(); err != nil {
		return err
	}
	final := filepath.Join(s.Root(), id)
	deleting := filepath.Join(s.Root(), ".deleting-"+id)
	if _, err := os.Lstat(deleting); err == nil {
		if _, finalErr := os.Lstat(final); finalErr == nil {
			return fmt.Errorf("archive exists in both published and deleting states")
		} else if !errors.Is(finalErr, os.ErrNotExist) {
			return finalErr
		}
		if _, err := validateDirectory(deleting, id); err != nil {
			return fmt.Errorf("validate interrupted archive deletion: %w", err)
		}
		return removeDeletingArchive(s.Root(), deleting)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := validateDirectory(final, id); err != nil {
		return err
	}
	if err := os.Rename(final, deleting); err != nil {
		return fmt.Errorf("mark archive for deletion: %w", err)
	}
	if err := work.SyncDirectory(s.Root()); err != nil {
		return fmt.Errorf("sync archive deletion intent: %w", err)
	}
	return removeDeletingArchive(s.Root(), deleting)
}

func removeDeletingArchive(root, path string) error {
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("delete archive: %w", err)
	}
	if err := work.SyncDirectory(root); err != nil {
		return fmt.Errorf("sync archive deletion: %w", err)
	}
	return nil
}

func validateDirectory(path, id string) (Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Manifest{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("archive path is not a real directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Manifest{}, err
	}
	allowed := map[string]bool{"archive.json": true}
	var manifest Manifest
	if err := work.LoadJSON(filepath.Join(path, "archive.json"), &manifest); err != nil {
		return Manifest{}, fmt.Errorf("read archive manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion || manifest.ArchiveID != id || manifest.WorkID == "" || manifest.RemovalID == "" || manifest.CreatedAt.IsZero() {
		return Manifest{}, fmt.Errorf("archive manifest is invalid")
	}
	if _, err := work.ParseName(manifest.WorkName); err != nil || !strings.HasPrefix(id, manifest.WorkName+"-") {
		return Manifest{}, fmt.Errorf("archive manifest Work identity is invalid")
	}
	for _, file := range manifest.Files {
		if file.Name != "new-work.json" && file.Name != "sync-work.json" && file.Name != "remove-work.json" {
			return Manifest{}, fmt.Errorf("archive manifest contains unsafe file %q", file.Name)
		}
		if allowed[file.Name] {
			return Manifest{}, fmt.Errorf("archive manifest contains duplicate file %q", file.Name)
		}
		allowed[file.Name] = true
		data, err := work.ReadRegularFile(filepath.Join(path, file.Name))
		if err != nil {
			return Manifest{}, err
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != file.Size || hex.EncodeToString(digest[:]) != file.SHA256 {
			return Manifest{}, fmt.Errorf("archive file %s failed digest verification", file.Name)
		}
	}
	if !allowed["new-work.json"] || !allowed["remove-work.json"] {
		return Manifest{}, fmt.Errorf("archive is missing required operation metadata")
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return Manifest{}, err
		}
		if !info.Mode().IsRegular() || !allowed[entry.Name()] {
			return Manifest{}, fmt.Errorf("archive contains unexpected or unsafe entry %s", entry.Name())
		}
	}
	return manifest, nil
}

func validateArchiveID(id string) error {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) || !archiveIDPattern.MatchString(id) {
		return fmt.Errorf("invalid archive ID %q", id)
	}
	return nil
}

// ValidateID validates an archive identifier without accessing the filesystem.
func ValidateID(id string) error {
	return validateArchiveID(id)
}

func (s Store) ensureRoot() error {
	if s.ControlRoot == "" {
		return fmt.Errorf("archive control root is empty")
	}
	controlRoot, err := filepath.Abs(s.ControlRoot)
	if err != nil {
		return err
	}
	paths := []string{controlRoot, filepath.Join(controlRoot, "archives"), filepath.Join(controlRoot, "archives", "removed-work")}
	for index, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			created := false
			if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			} else if err == nil {
				created = true
			}
			info, err = os.Lstat(path)
			if err == nil && created {
				if syncErr := work.SyncDirectory(filepath.Dir(path)); syncErr != nil {
					return fmt.Errorf("sync archive path creation: %w", syncErr)
				}
			}
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive path component is unsafe: %s", path)
		}
	}
	return nil
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
