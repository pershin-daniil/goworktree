package inspectworks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func TestInspectUnionsDirectoriesAndOperationRecordsInStableOrder(t *testing.T) {
	t.Parallel()

	worksRoot := filepath.Join(t.TempDir(), "works")
	controlRoot := t.TempDir()
	mustMkdir(t, worksRoot)
	mustMkdir(t, filepath.Join(worksRoot, "work-b"))
	mustMkdir(t, filepath.Join(worksRoot, "work-a"))
	mustMkdir(t, filepath.Join(worksRoot, ".invalid"))
	if err := os.WriteFile(filepath.Join(worksRoot, ".DS_Store"), []byte("metadata"), 0o644); err != nil {
		t.Fatal(err)
	}
	operationDirectory := filepath.Join(controlRoot, "operations", "new-work")
	mustMkdir(t, operationDirectory)
	operationPath := filepath.Join(operationDirectory, strings.Repeat("a", 64)+".json")
	if err := os.WriteFile(operationPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	operationPath, _ = filepath.EvalSymlinks(operationPath)

	fakeWorks := &fakeWorkInspector{}
	inspectedAt := time.Date(2026, 8, 15, 9, 30, 0, 0, time.UTC)
	inspector := Inspector{
		Works: fakeWorks,
		Operations: fakeOperationReader{records: map[string]newwork.OperationRecord{
			operationPath: operationRecord(worksRoot, controlRoot, "work-c"),
		}},
		Now: func() time.Time { return inspectedAt },
	}
	snapshot, err := inspector.Inspect(context.Background(), Request{WorksRoot: worksRoot, ControlRoot: controlRoot})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.InspectedAt.Equal(inspectedAt) {
		t.Fatalf("InspectedAt = %s", snapshot.InspectedAt)
	}
	if got, want := workNames(snapshot.Works), ".invalid,work-a,work-b,work-c"; got != want {
		t.Fatalf("Work order = %q, want %q; problems = %+v", got, want, snapshot.Problems)
	}
	if len(snapshot.Works[0].Problems) != 1 || snapshot.Works[0].Problems[0].Code != ProblemEntryNameInvalid {
		t.Fatalf("invalid entry = %+v", snapshot.Works[0])
	}
	if fakeWorks.names() != "work-a,work-b,work-c" {
		t.Fatalf("inspected names = %q", fakeWorks.names())
	}
	if !snapshot.Works[3].FromOperation || snapshot.Works[3].FromDirectory {
		t.Fatalf("operation-only Work = %+v", snapshot.Works[3])
	}
}

func TestInspectRecordFailureDoesNotHideDirectoryWorks(t *testing.T) {
	t.Parallel()

	worksRoot := filepath.Join(t.TempDir(), "works")
	controlRoot := t.TempDir()
	mustMkdir(t, filepath.Join(worksRoot, "visible"))
	operationDirectory := filepath.Join(controlRoot, "operations", "new-work")
	mustMkdir(t, operationDirectory)
	badPath := filepath.Join(operationDirectory, "broken.json")
	if err := os.WriteFile(badPath, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	badPath, _ = filepath.EvalSymlinks(badPath)

	inspector := Inspector{
		Works:      &fakeWorkInspector{},
		Operations: fakeOperationReader{failures: map[string]error{badPath: errors.New("invalid JSON")}},
	}
	snapshot, err := inspector.Inspect(context.Background(), Request{WorksRoot: worksRoot, ControlRoot: controlRoot})
	if err != nil {
		t.Fatal(err)
	}
	if got := workNames(snapshot.Works); got != "visible" {
		t.Fatalf("Works = %q", got)
	}
	if len(snapshot.Problems) != 1 || snapshot.Problems[0].Code != ProblemOperationRecordInvalid {
		t.Fatalf("collection problems = %+v", snapshot.Problems)
	}
}

func TestInspectAbsentRootsAreAValidEmptySnapshot(t *testing.T) {
	t.Parallel()

	inspector := Inspector{Works: &fakeWorkInspector{}, Operations: fakeOperationReader{}}
	snapshot, err := inspector.Inspect(context.Background(), Request{
		WorksRoot: filepath.Join(t.TempDir(), "missing-works"), ControlRoot: filepath.Join(t.TempDir(), "missing-control"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Works) != 0 || len(snapshot.Problems) != 0 {
		t.Fatalf("empty snapshot = %+v", snapshot)
	}
}

func TestInspectOneFailureDoesNotHideOtherWorks(t *testing.T) {
	t.Parallel()

	worksRoot := filepath.Join(t.TempDir(), "works")
	controlRoot := t.TempDir()
	mustMkdir(t, filepath.Join(worksRoot, "broken"))
	mustMkdir(t, filepath.Join(worksRoot, "visible"))
	fakeWorks := &fakeWorkInspector{failures: map[string]error{"broken": errors.New("Git unavailable")}}
	inspector := Inspector{Works: fakeWorks, Operations: fakeOperationReader{}}
	snapshot, err := inspector.Inspect(context.Background(), Request{WorksRoot: worksRoot, ControlRoot: controlRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Works) != 2 || snapshot.Works[0].Snapshot != nil || snapshot.Works[1].Snapshot == nil {
		t.Fatalf("independent results = %+v", snapshot.Works)
	}
	if len(snapshot.Works[0].Problems) != 1 || snapshot.Works[0].Problems[0].Code != ProblemWorkInspectionFailed {
		t.Fatalf("failed Work = %+v", snapshot.Works[0])
	}
}

type fakeWorkInspector struct {
	requests []inspectwork.Request
	failures map[string]error
}

func (f *fakeWorkInspector) Inspect(_ context.Context, request inspectwork.Request) (inspectwork.Snapshot, error) {
	f.requests = append(f.requests, request)
	if err := f.failures[request.Name]; err != nil {
		return inspectwork.Snapshot{}, err
	}
	name, _ := work.ParseName(request.Name)
	return inspectwork.Snapshot{WorkName: name, WorkRoot: filepath.Join(request.WorksRoot, request.Name)}, nil
}

func (f *fakeWorkInspector) names() string {
	names := make([]string, 0, len(f.requests))
	for _, request := range f.requests {
		names = append(names, request.Name)
	}
	return strings.Join(names, ",")
}

type fakeOperationReader struct {
	records  map[string]newwork.OperationRecord
	failures map[string]error
}

func (f fakeOperationReader) Load(path string) (newwork.OperationRecord, error) {
	if err := f.failures[path]; err != nil {
		return newwork.OperationRecord{}, err
	}
	record, ok := f.records[path]
	if !ok {
		return newwork.OperationRecord{}, os.ErrNotExist
	}
	return record, nil
}

func operationRecord(worksRoot, controlRoot, name string) newwork.OperationRecord {
	workName, _ := work.ParseName(name)
	workID := work.NewIdentity(worksRoot, workName)
	return newwork.OperationRecord{Plan: newwork.Plan{
		WorkID: workID, WorkName: workName, WorkRoot: filepath.Join(worksRoot, name),
		OperationRecordPath: filepath.Join(controlRoot, "operations", "new-work", workID.String()+".json"),
	}}
}

func workNames(works []Work) string {
	names := make([]string, 0, len(works))
	for _, entry := range works {
		names = append(names, entry.Name)
	}
	return strings.Join(names, ",")
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
