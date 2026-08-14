package newwork

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
)

func TestExecutorCreatesAndVerifiesMultiRepositoryWork(t *testing.T) {
	t.Parallel()

	api := newLocalRepository(t, true)
	billing := newLocalRepository(t, true)
	commitFile(t, api, "go.mod", "module example.test/api\n\ngo 1.22\n", "set api Go version")
	commitFile(t, billing, "go.mod", "module example.test/billing\n\ngo 1.24.1 // supported patch form\n", "set billing Go version")
	apiHead := gitOutput(t, api, "rev-parse", "HEAD")
	billingHead := gitOutput(t, billing, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(api, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, api, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(api, "README.md"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(api, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	apiStatus := gitOutput(t, api, "status", "--porcelain")
	billingStatus := gitOutput(t, billing, "status", "--porcelain")

	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "EVOVPC-4000", map[string]Repository{
		"api":     {SourcePath: api, Folder: "api", BasePreference: "main"},
		"billing": {SourcePath: billing, Folder: "billing", BasePreference: "main"},
	})

	executor := testExecutor(controlRoot, SystemGit{})
	result, err := executor.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Status != ExecutionCreated || len(result.VerifiedRepositories) != 2 {
		t.Fatalf("result = %+v", result)
	}

	wantGoWork := "go 1.24.1\n\nuse (\n\t./api\n\t./billing\n)\n"
	data, err := os.ReadFile(filepath.Join(plan.WorkRoot, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != wantGoWork {
		t.Fatalf("go.work:\n%s\nwant:\n%s", data, wantGoWork)
	}

	var manifest work.Manifest
	if err := work.LoadJSON(plan.ManifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if manifest.WorkID != plan.WorkID || manifest.Name != plan.WorkName || len(manifest.Repositories) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}

	record, err := (OperationStore{}).Load(plan.OperationRecordPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.Phase != PhaseCreated || record.Harness.State != StepVerified || record.Harness.GoVersion != "1.24.1" {
		t.Fatalf("operation = %+v", record)
	}
	for _, repo := range plan.Repositories {
		checkout, err := (SystemGit{}).InspectCheckout(context.Background(), repo.Destination)
		if err != nil {
			t.Fatal(err)
		}
		if checkout.FullRef != repo.TargetBranchRef || checkout.HeadOID != repo.BaseOID || checkout.Identity.CommonDir != repo.GitCommonDir {
			t.Fatalf("checkout %s = %+v", repo.ID, checkout)
		}
	}
	if got := gitOutput(t, api, "rev-parse", "HEAD"); got != apiHead {
		t.Fatalf("api source HEAD changed: got %s, want %s", got, apiHead)
	}
	if got := gitOutput(t, billing, "rev-parse", "HEAD"); got != billingHead {
		t.Fatalf("billing source HEAD changed: got %s, want %s", got, billingHead)
	}
	if got := gitOutput(t, api, "status", "--porcelain"); got != apiStatus {
		t.Fatalf("api source status changed: %q", got)
	}
	if got := gitOutput(t, billing, "status", "--porcelain"); got != billingStatus {
		t.Fatalf("billing source status changed: %q", got)
	}

	beforeResume, err := os.ReadFile(plan.OperationRecordPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := executor.Resume(context.Background(), plan.OperationRecordPath)
	if err != nil || resumed.Status != ExecutionCreated {
		t.Fatalf("idempotent Resume = %+v, %v", resumed, err)
	}
	afterResume, err := os.ReadFile(plan.OperationRecordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeResume) != string(afterResume) {
		t.Fatal("Resume rewrote an already created operation")
	}
	commitFile(t, plan.Repositories[0].Destination, "work.txt", "continued work\n", "continue normal work")
	resumed, err = executor.Resume(context.Background(), plan.OperationRecordPath)
	if err != nil || resumed.Status != ExecutionCreated {
		t.Fatalf("Resume after normal branch progress = %+v, %v", resumed, err)
	}
	gitRun(t, plan.Repositories[0].Destination, "checkout", "--detach")
	broken, err := executor.Resume(context.Background(), plan.OperationRecordPath)
	if err == nil || broken.Status != ExecutionPartial {
		t.Fatalf("Resume trusted stale created status after detached HEAD: %+v, %v", broken, err)
	}
	assertProblemCode(t, err, CodeStateConflict)
}

func TestExecutorResumesWorktreeCreatedBeforeCheckpoint(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, true)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-1", map[string]Repository{
		"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	interruptingGit := &cancelAfterCreateGit{SystemGit: SystemGit{}, cancel: cancel}
	executor := testExecutor(controlRoot, interruptingGit)
	result, err := executor.Execute(ctx, plan)
	if err == nil || result.Status != ExecutionInterrupted {
		t.Fatalf("interrupted Execute = %+v, %v", result, err)
	}
	record, loadErr := (OperationStore{}).Load(plan.OperationRecordPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if record.Phase != PhaseInterrupted || record.Repositories[0].State != StepIntent {
		t.Fatalf("interrupted record = %+v", record)
	}
	if _, err := os.Stat(plan.Repositories[0].Destination); err != nil {
		t.Fatalf("Git command did not create worktree before interruption: %v", err)
	}

	normal := testExecutor(controlRoot, SystemGit{})
	resumed, err := normal.Resume(context.Background(), plan.OperationRecordPath)
	if err != nil || resumed.Status != ExecutionCreated {
		t.Fatalf("Resume = %+v, %v", resumed, err)
	}
	worktrees, err := (SystemGit{}).ListWorktrees(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("worktrees after Resume = %+v, want primary plus one linked", worktrees)
	}
}

func TestExecutorResumesBranchOnlyStateAndLeavesLaterRepositoryPending(t *testing.T) {
	t.Parallel()

	first := newLocalRepository(t, false)
	second := newLocalRepository(t, false)
	third := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-2", map[string]Repository{
		"first":  {SourcePath: first, Folder: "first", BasePreference: "main"},
		"second": {SourcePath: second, Folder: "second", BasePreference: "main"},
		"third":  {SourcePath: third, Folder: "third", BasePreference: "main"},
	})

	executor := testExecutor(controlRoot, &branchOnlyFailureGit{SystemGit: SystemGit{}, t: t})
	result, err := executor.Execute(context.Background(), plan)
	if err == nil || result.Status != ExecutionPartial {
		t.Fatalf("partial Execute = %+v, %v", result, err)
	}
	record, loadErr := (OperationStore{}).Load(plan.OperationRecordPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if record.Repositories[0].State != StepVerified || record.Repositories[1].State != StepIntent || record.Repositories[2].State != StepPending {
		t.Fatalf("repository checkpoints = %+v; execution error=%v; last problem=%+v", record.Repositories, err, record.LastProblem)
	}
	if _, exists, branchErr := (SystemGit{}).LocalBranchOID(context.Background(), third, "work-2"); branchErr != nil || exists {
		t.Fatalf("later repository mutated: exists=%v err=%v", exists, branchErr)
	}

	normal := testExecutor(controlRoot, SystemGit{})
	resumed, err := normal.Resume(context.Background(), plan.OperationRecordPath)
	if err != nil || resumed.Status != ExecutionCreated {
		t.Fatalf("Resume branch-only state = %+v, %v", resumed, err)
	}
	if _, err := os.Stat(plan.Repositories[0].Destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Repositories[1].Destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plan.Repositories[2].Destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plan.WorkRoot, "go.work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Work without root modules produced go.work: %v", err)
	}
}

func TestExecutorRevalidatesPlanBeforeRecordingIntent(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-3", map[string]Repository{
		"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
	})
	gitRun(t, repo, "branch", "work-3", plan.Repositories[0].BaseOID)

	result, err := testExecutor(controlRoot, SystemGit{}).Execute(context.Background(), plan)
	if err == nil || result.Status != "" {
		t.Fatalf("Execute with invalidated plan = %+v, %v", result, err)
	}
	assertProblemCode(t, err, CodeStateConflict)
	if _, err := os.Lstat(plan.OperationRecordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid plan created operation record: %v", err)
	}
	if _, err := os.Lstat(plan.WorkRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid plan created Work root: %v", err)
	}
}

func TestExecutorStopsBeforeMutationWhenWorkIsLocked(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-locked", map[string]Repository{
		"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
	})
	set := lockops.Set{Root: controlRoot}
	lease, err := set.Acquire(context.Background(), "works", []string{plan.WorkID.String()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Release() }()

	result, err := testExecutor(controlRoot, SystemGit{}).Execute(context.Background(), plan)
	if err == nil || result.Status != "" {
		t.Fatalf("locked Execute = %+v, %v", result, err)
	}
	assertProblemCode(t, err, CodeLocked)
	if _, err := os.Lstat(plan.OperationRecordPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locked execution created operation record: %v", err)
	}
}

func TestExecutorDoesNotAdoptWorkRootThatAppearsAfterValidation(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-race", map[string]Repository{
		"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
	})
	executor := testExecutor(controlRoot, SystemGit{})
	executor.Store = createWorkRootAfterIntentStore{OperationStore: OperationStore{}}

	result, err := executor.Execute(context.Background(), plan)
	if err == nil || result.Status != ExecutionPartial {
		t.Fatalf("Execute with Work-root race = %+v, %v", result, err)
	}
	assertProblemCode(t, err, CodeStateConflict)
	if _, err := os.Lstat(plan.ManifestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("executor adopted raced Work root and wrote manifest: %v", err)
	}
}

func TestExecutorDoesNotAdoptWorktreeThatAppearsAfterStepIntent(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-step-race", map[string]Repository{
		"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
	})
	executor := testExecutor(controlRoot, SystemGit{})
	executor.Store = &createWorktreeAfterStepIntentStore{OperationStore: OperationStore{}, git: SystemGit{}}

	result, err := executor.Execute(context.Background(), plan)
	if err == nil || result.Status != ExecutionPartial {
		t.Fatalf("Execute with repository race = %+v, %v", result, err)
	}
	assertProblemCode(t, err, CodeStateConflict)
	record, loadErr := (OperationStore{}).Load(plan.OperationRecordPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if record.Repositories[0].State != StepIntent {
		t.Fatalf("raced worktree was adopted: %+v", record.Repositories[0])
	}
}

func TestExecutorResumeOwnsEmptyWorkRootAfterRecordedInterruption(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-owned-root", map[string]Repository{
		"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
	})
	record := newOperationRecord(plan, strings.Repeat("b", 32), time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC))
	if err := (OperationStore{}).Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(plan.WorkRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.WorkRoot, ".goworktree.json-crashed-write"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := testExecutor(controlRoot, SystemGit{}).Resume(context.Background(), plan.OperationRecordPath)
	if err != nil || result.Status != ExecutionCreated {
		t.Fatalf("Resume recorded empty Work root = %+v, %v", result, err)
	}
	if _, err := os.Stat(plan.ManifestPath); err != nil {
		t.Fatal(err)
	}
}

func TestExecutorUsesConfirmedOIDAndNeverMutatesRemoteRefs(t *testing.T) {
	t.Parallel()

	fixture := newRemoteFixture(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	plan := buildOfflinePlan(t, worksRoot, controlRoot, "work-oid", map[string]Repository{
		"repo": {SourcePath: fixture.source, Folder: "repo", Remote: "origin", BasePreference: "main"},
	})
	confirmedOID := plan.Repositories[0].BaseOID

	fixture.commitAndPush("remote.txt", "advanced\n", "advance remote")
	fixture.run(fixture.source, "pull", "--ff-only")
	advancedOID := fixture.output(fixture.source, "rev-parse", "HEAD")
	if advancedOID == confirmedOID {
		t.Fatal("fixture did not advance the base branch after planning")
	}
	remoteBefore := fixture.output(fixture.origin, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")

	result, err := testExecutor(controlRoot, SystemGit{}).Execute(context.Background(), plan)
	if err != nil || result.Status != ExecutionCreated {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	checkout, err := (SystemGit{}).InspectCheckout(context.Background(), plan.Repositories[0].Destination)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.HeadOID != confirmedOID {
		t.Fatalf("worktree started at %s, want confirmed %s; current main is %s", checkout.HeadOID, confirmedOID, advancedOID)
	}
	remoteAfter := fixture.output(fixture.origin, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads")
	if remoteAfter != remoteBefore {
		t.Fatalf("remote refs changed:\nbefore: %s\nafter:  %s", remoteBefore, remoteAfter)
	}
	cmd := exec.Command("git", "--git-dir="+fixture.origin, "show-ref", "--verify", "--quiet", "refs/heads/work-oid")
	if err := cmd.Run(); err == nil {
		t.Fatal("New Work created a remote work branch")
	}
}

type cancelAfterCreateGit struct {
	SystemGit
	cancel context.CancelFunc
}

func (g *cancelAfterCreateGit) CreateWorktreeAtOID(ctx context.Context, repo, destination, branchRef, oid string) error {
	if err := g.SystemGit.CreateWorktreeAtOID(ctx, repo, destination, branchRef, oid); err != nil {
		return err
	}
	g.cancel()
	return context.Canceled
}

type branchOnlyFailureGit struct {
	SystemGit
	t     *testing.T
	calls int
}

type createWorkRootAfterIntentStore struct {
	OperationStore
}

type createWorktreeAfterStepIntentStore struct {
	OperationStore
	git     SystemGit
	created bool
}

func (s *createWorktreeAfterStepIntentStore) Save(record OperationRecord) error {
	if err := s.OperationStore.Save(record); err != nil {
		return err
	}
	if s.created || len(record.Repositories) == 0 || record.Repositories[0].State != StepIntent {
		return nil
	}
	s.created = true
	plan := record.Plan.Repositories[0]
	return s.git.CreateWorktreeAtOID(context.Background(), plan.SourcePath, plan.Destination, plan.TargetBranchRef, plan.BaseOID)
}

func (s createWorkRootAfterIntentStore) Create(record OperationRecord) error {
	if err := s.OperationStore.Create(record); err != nil {
		return err
	}
	return os.Mkdir(record.Plan.WorkRoot, 0o755)
}

func (g *branchOnlyFailureGit) CreateWorktreeAtOID(ctx context.Context, repo, destination, branchRef, oid string) error {
	g.t.Helper()
	g.calls++
	if g.calls != 2 {
		return g.SystemGit.CreateWorktreeAtOID(ctx, repo, destination, branchRef, oid)
	}
	gitRun(g.t, repo, "update-ref", branchRef, oid)
	return errors.New("injected failure after branch creation")
}

func buildOfflinePlan(t *testing.T, worksRoot, controlRoot, name string, repositories map[string]Repository) Plan {
	t.Helper()
	ids := make([]string, 0, len(repositories))
	for id := range repositories {
		ids = append(ids, id)
	}
	plan, err := (Planner{Git: SystemGit{}}).Build(context.Background(), Catalog{
		WorksRoot:    worksRoot,
		ControlRoot:  controlRoot,
		Repositories: repositories,
	}, Request{Name: name, RepositoryIDs: ids, Mode: ModeOffline})
	if err != nil {
		t.Fatalf("Build plan: %v", err)
	}
	return plan
}

func testExecutor(controlRoot string, git ExecutionGit) Executor {
	fixedTime := time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC)
	return Executor{
		Git:     git,
		Locker:  FileExecutionLocker{Set: lockops.Set{Root: controlRoot}},
		Store:   OperationStore{},
		Now:     func() time.Time { return fixedTime },
		NewID:   func() (string, error) { return strings.Repeat("a", 32), nil },
		Harness: GoWorkHarness{},
	}
}

func commitFile(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", name)
	gitRun(t, repo, "commit", "-m", message)
}
