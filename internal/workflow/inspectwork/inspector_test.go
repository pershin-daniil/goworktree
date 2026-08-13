package inspectwork

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func TestInspectHealthyWorkReportsDirtyFactsWithoutProblems(t *testing.T) {
	t.Parallel()

	fixture := createCompletedWork(t, "inspect-healthy", map[string]bool{"api": true})
	repo := fixture.Plan.Repositories[0]
	writeFile(t, repo.Destination, "staged.txt", "staged\n")
	gitRun(t, repo.Destination, "add", "staged.txt")
	writeFile(t, repo.Destination, "README.md", "unstaged\n")
	writeFile(t, repo.Destination, "untracked.txt", "untracked\n")
	writeFile(t, repo.Destination, "ignored.tmp", "ignored\n")

	inspectedAt := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	snapshot, err := fixture.inspect(func() time.Time { return inspectedAt })
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.InspectedAt.Equal(inspectedAt) || snapshot.Manifest.State != MetadataValid ||
		snapshot.Operation.State != MetadataValid || snapshot.Operation.Phase != string(newwork.PhaseCreated) {
		t.Fatalf("metadata snapshot = %+v", snapshot)
	}
	if snapshot.IntentSource != IntentManifest || len(snapshot.Repositories) != 1 {
		t.Fatalf("intent snapshot = %+v", snapshot)
	}
	observed := snapshot.Repositories[0]
	if !observed.SourceKnown || !observed.BranchKnown || !observed.BranchExists ||
		!observed.RegistrationsKnown || !observed.CheckoutKnown || !observed.WorkingTreeKnown ||
		!observed.GitOperationsKnown {
		t.Fatalf("repository contains unknown facts: %+v", observed)
	}
	wantStatus := gitops.WorkingTreeStatus{Staged: 1, Unstaged: 1, Untracked: 1, Ignored: 1}
	if observed.WorkingTree != wantStatus {
		t.Fatalf("working tree = %+v, want %+v", observed.WorkingTree, wantStatus)
	}
	if len(observed.GitOperations) != 0 || len(snapshot.Problems) != 0 {
		t.Fatalf("healthy dirty Work has problems: %+v", snapshot.Problems)
	}
	if !snapshot.Harness.VerifiedAgainstOperation {
		t.Fatal("go.work was not verified against recorded intent")
	}
}

func TestInspectMissingWorktreeDoesNotHideOtherRepositories(t *testing.T) {
	t.Parallel()

	fixture := createCompletedWork(t, "inspect-missing", map[string]bool{"api": false, "billing": false})
	missing := fixture.Plan.Repositories[0]
	if err := os.RemoveAll(missing.Destination); err != nil {
		t.Fatal(err)
	}

	snapshot, err := fixture.inspect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Repositories) != 2 {
		t.Fatalf("repositories = %d, want 2", len(snapshot.Repositories))
	}
	first := snapshot.Repositories[0]
	if first.DestinationKind != PathMissing || !hasRepositoryProblem(first, ProblemDestinationMissing) ||
		!hasRepositoryProblem(first, ProblemRegistrationStale) {
		t.Fatalf("missing repository snapshot = %+v", first)
	}
	second := snapshot.Repositories[1]
	if !second.CheckoutKnown || !second.WorkingTreeKnown || len(second.Problems) != 0 {
		t.Fatalf("healthy repository was hidden or marked broken: %+v", second)
	}
}

func TestInspectCorruptManifestFallsBackToOperationIntent(t *testing.T) {
	t.Parallel()

	fixture := createCompletedWork(t, "inspect-corrupt-manifest", map[string]bool{"api": true})
	if err := os.WriteFile(fixture.Plan.ManifestPath, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot, err := fixture.inspect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest.State != MetadataInvalid || snapshot.Operation.State != MetadataValid ||
		snapshot.IntentSource != IntentOperationRecord {
		t.Fatalf("fallback metadata = %+v", snapshot)
	}
	if !hasProblem(snapshot, ProblemManifestInvalid) {
		t.Fatalf("manifest corruption not reported: %+v", snapshot.Problems)
	}
	if len(snapshot.Repositories) != 1 || !snapshot.Repositories[0].CheckoutKnown {
		t.Fatalf("operation intent was not inspected: %+v", snapshot.Repositories)
	}
}

func TestInspectCorruptOperationStillUsesManifest(t *testing.T) {
	t.Parallel()

	fixture := createCompletedWork(t, "inspect-corrupt-operation", map[string]bool{"api": false})
	if err := os.WriteFile(fixture.Plan.OperationRecordPath, []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := fixture.inspect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest.State != MetadataValid || snapshot.Operation.State != MetadataInvalid ||
		snapshot.IntentSource != IntentManifest {
		t.Fatalf("metadata fallback = %+v", snapshot)
	}
	if !hasProblem(snapshot, ProblemOperationInvalid) || len(snapshot.Repositories) != 1 || !snapshot.Repositories[0].CheckoutKnown {
		t.Fatalf("operation corruption handling = %+v", snapshot)
	}
}

func TestInspectIncompleteOperationSuggestsResume(t *testing.T) {
	t.Parallel()

	repo := newRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildPlan(t, worksRoot, controlRoot, "inspect-resume", map[string]string{"api": repo})
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	record := newwork.OperationRecord{
		SchemaVersion: newwork.OperationSchemaVersion,
		OperationID:   strings.Repeat("d", 32),
		Kind:          "new-work",
		WorkID:        plan.WorkID,
		Phase:         newwork.PhaseRecordingIntent,
		Plan:          plan,
		Repositories:  []newwork.RepositoryCheckpoint{{ID: "api", State: newwork.StepPending}},
		Harness:       newwork.HarnessCheckpoint{State: newwork.StepPending},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := (newwork.OperationStore{}).Create(record); err != nil {
		t.Fatal(err)
	}

	snapshot, err := testInspector(nil).Inspect(context.Background(), Request{
		WorksRoot: worksRoot, ControlRoot: controlRoot, Name: "inspect-resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Operation.State != MetadataValid || !snapshot.Operation.ResumeSuggested ||
		snapshot.IntentSource != IntentOperationRecord {
		t.Fatalf("incomplete operation = %+v", snapshot)
	}
	if problem, ok := findProblem(snapshot, ProblemNewWorkIncomplete); !ok || problem.Next != ActionResumeNewWork {
		t.Fatalf("Resume action missing: %+v", snapshot.Problems)
	}
	if problem, ok := findProblem(snapshot, ProblemWorkRootMissing); !ok || problem.Next != ActionResumeNewWork {
		t.Fatalf("missing root does not route to Resume: %+v", snapshot.Problems)
	}
	if len(snapshot.Repositories) != 1 || snapshot.Repositories[0].IntentSource != IntentOperationRecord {
		t.Fatalf("recorded repository was not inspected: %+v", snapshot.Repositories)
	}
}

func TestInspectMismatchedManifestBlocksResume(t *testing.T) {
	t.Parallel()

	fixture := createCompletedWork(t, "inspect-mismatched-resume", map[string]bool{"api": false})
	var manifest work.Manifest
	if err := work.LoadJSON(fixture.Plan.ManifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.NewWorkOperationID = strings.Repeat("b", 32)
	if err := work.ReplaceJSON(fixture.Plan.ManifestPath, manifest, 0o644, nil); err != nil {
		t.Fatal(err)
	}
	var record newwork.OperationRecord
	if err := work.LoadJSON(fixture.Plan.OperationRecordPath, &record); err != nil {
		t.Fatal(err)
	}
	record.Phase = newwork.PhaseInterrupted
	if err := (newwork.OperationStore{}).Save(record); err != nil {
		t.Fatal(err)
	}

	snapshot, err := fixture.inspect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProblem(snapshot, ProblemOperationMismatch) || snapshot.Operation.ResumeSuggested {
		t.Fatalf("mismatched records allowed Resume: %+v", snapshot)
	}
	problem, ok := findProblem(snapshot, ProblemNewWorkIncomplete)
	if !ok || problem.Next != ActionRepairWork {
		t.Fatalf("incomplete mismatched operation recovery = %+v", snapshot.Problems)
	}
}

func TestInspectReportsRebaseAndConflict(t *testing.T) {
	t.Parallel()

	fixture := createCompletedWork(t, "inspect-rebase", map[string]bool{"api": false})
	planned := fixture.Plan.Repositories[0]
	writeFile(t, planned.Destination, "README.md", "work change\n")
	gitRun(t, planned.Destination, "add", "README.md")
	gitRun(t, planned.Destination, "commit", "-m", "work change")
	writeFile(t, planned.SourcePath, "README.md", "base change\n")
	gitRun(t, planned.SourcePath, "add", "README.md")
	gitRun(t, planned.SourcePath, "commit", "-m", "base change")
	gitRunExpectFailure(t, planned.Destination, "rebase", "main")

	snapshot, err := fixture.inspect(nil)
	if err != nil {
		t.Fatal(err)
	}
	repository := snapshot.Repositories[0]
	if !repository.WorkingTreeKnown || repository.WorkingTree.Conflicted != 1 {
		t.Fatalf("conflict state = %+v", repository.WorkingTree)
	}
	if !repository.GitOperationsKnown || !containsOperation(repository.GitOperations, gitops.OperationRebase) {
		t.Fatalf("active operations = %+v", repository.GitOperations)
	}
	problem, ok := findRepositoryProblem(repository, ProblemActiveGitOperation)
	if !ok || problem.Next != ActionResolveGitOperation {
		t.Fatalf("rebase recovery problem = %+v", repository.Problems)
	}
}

func TestInspectLegacyManifestReadsRepositoryFactsWithoutMigration(t *testing.T) {
	t.Parallel()

	name := "inspect-legacy"
	source := newRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	workRoot := filepath.Join(worksRoot, name)
	mustMkdir(t, workRoot)
	destination := filepath.Join(workRoot, "api")
	gitRun(t, source, "worktree", "add", "-b", name, destination, "main")
	manifest := legacyManifest{
		Name: name, CreatedAt: "2026-08-15T09:00:00Z",
		Repos: []legacyRepositoryIntent{{
			ID: "api", Folder: "api", Path: source, Branch: name, Base: "main", Status: "ready",
		}},
	}
	manifestPath := filepath.Join(workRoot, ".goworktree.json")
	if err := work.CreateJSON(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := testInspector(nil).Inspect(context.Background(), Request{
		WorksRoot: worksRoot, ControlRoot: t.TempDir(), Name: name,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest.State != MetadataLegacy || snapshot.IntentSource != IntentLegacyManifest {
		t.Fatalf("legacy metadata = %+v", snapshot)
	}
	if len(snapshot.Repositories) != 1 || !snapshot.Repositories[0].CheckoutKnown || !snapshot.Repositories[0].WorkingTreeKnown {
		t.Fatalf("legacy repository facts = %+v", snapshot.Repositories)
	}
	if len(snapshot.Problems) != 0 {
		t.Fatalf("healthy legacy Work problems = %+v", snapshot.Problems)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("Inspect Work rewrote legacy manifest")
	}
}

func TestInspectRejectsEmptyRoots(t *testing.T) {
	t.Parallel()

	inspector := testInspector(nil)
	if _, err := inspector.Inspect(context.Background(), Request{
		ControlRoot: t.TempDir(), Name: "inspect-empty-works-root",
	}); err == nil || !strings.Contains(err.Error(), "works root is empty") {
		t.Fatalf("empty works root error = %v", err)
	}
	if _, err := inspector.Inspect(context.Background(), Request{
		WorksRoot: t.TempDir(), Name: "inspect-empty-control-root",
	}); err == nil || !strings.Contains(err.Error(), "control root is empty") {
		t.Fatalf("empty control root error = %v", err)
	}
}

type completedWorkFixture struct {
	WorksRoot   string
	ControlRoot string
	Plan        newwork.Plan
}

func createCompletedWork(t *testing.T, name string, repositories map[string]bool) completedWorkFixture {
	t.Helper()
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	paths := make(map[string]string, len(repositories))
	for id, module := range repositories {
		paths[id] = newRepository(t, module)
	}
	plan := buildPlan(t, worksRoot, controlRoot, name, paths)
	executor := newwork.Executor{
		Git:     newwork.SystemGit{},
		Locker:  newwork.FileExecutionLocker{Set: lockops.Set{Root: controlRoot}},
		Store:   newwork.OperationStore{},
		Harness: newwork.GoWorkHarness{},
		NewID:   func() (string, error) { return strings.Repeat("a", 32), nil },
	}
	result, err := executor.Execute(context.Background(), plan)
	if err != nil || result.Status != newwork.ExecutionCreated {
		t.Fatalf("create completed Work: %+v, %v", result, err)
	}
	return completedWorkFixture{WorksRoot: worksRoot, ControlRoot: controlRoot, Plan: plan}
}

func buildPlan(t *testing.T, worksRoot, controlRoot, name string, paths map[string]string) newwork.Plan {
	t.Helper()
	ids := make([]string, 0, len(paths))
	repositories := make(map[string]newwork.Repository, len(paths))
	for id, path := range paths {
		ids = append(ids, id)
		repositories[id] = newwork.Repository{SourcePath: path, Folder: id, BasePreference: "main"}
	}
	plan, err := (newwork.Planner{Git: newwork.SystemGit{}}).Build(context.Background(), newwork.Catalog{
		WorksRoot: worksRoot, ControlRoot: controlRoot, Repositories: repositories,
	}, newwork.Request{Name: name, RepositoryIDs: ids, Mode: newwork.ModeOffline})
	if err != nil {
		t.Fatalf("build New Work plan: %v", err)
	}
	return plan
}

func (f completedWorkFixture) inspect(now func() time.Time) (Snapshot, error) {
	return testInspector(now).Inspect(context.Background(), Request{
		WorksRoot: f.WorksRoot, ControlRoot: f.ControlRoot, Name: f.Plan.WorkName.String(),
	})
}

func testInspector(now func() time.Time) Inspector {
	return Inspector{Git: SystemGit{}, Operations: SystemOperationReader{}, Now: now}
}

func newRepository(t *testing.T, module bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, repo)
	gitRun(t, repo, "init", "-b", "main")
	writeFile(t, repo, "README.md", "initial\n")
	writeFile(t, repo, ".gitignore", "*.tmp\n")
	if module {
		writeFile(t, repo, "go.mod", "module example.test/repo\n\ngo 1.23\n")
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func hasProblem(snapshot Snapshot, code ProblemCode) bool {
	_, ok := findProblem(snapshot, code)
	return ok
}

func findProblem(snapshot Snapshot, code ProblemCode) (Problem, bool) {
	for _, problem := range snapshot.Problems {
		if problem.Code == code {
			return problem, true
		}
	}
	return Problem{}, false
}

func hasRepositoryProblem(repository RepositorySnapshot, code ProblemCode) bool {
	_, ok := findRepositoryProblem(repository, code)
	return ok
}

func findRepositoryProblem(repository RepositorySnapshot, code ProblemCode) (Problem, bool) {
	for _, problem := range repository.Problems {
		if problem.Code == code {
			return problem, true
		}
	}
	return Problem{}, false
}

func containsOperation(operations []gitops.ActiveOperation, wanted gitops.ActiveOperation) bool {
	for _, operation := range operations {
		if operation == wanted {
			return true
		}
	}
	return false
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnvironment()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitRunExpectFailure(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnvironment()
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("git %s unexpectedly succeeded\n%s", strings.Join(args, " "), output)
	}
}

func gitTestEnvironment() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=inspect-test",
		"GIT_AUTHOR_EMAIL=inspect-test@example.com",
		"GIT_COMMITTER_NAME=inspect-test",
		"GIT_COMMITTER_EMAIL=inspect-test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}
