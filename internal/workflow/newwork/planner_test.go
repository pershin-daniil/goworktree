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

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
)

func TestPlannerBuildOnlineFromFetchedCommitWithoutWorkMutation(t *testing.T) {
	t.Parallel()

	fixture := newRemoteFixture(t, true)
	beforeSourceHead := fixture.output(fixture.source, "rev-parse", "HEAD")
	beforeRemoteRef := fixture.output(fixture.source, "rev-parse", "refs/remotes/origin/main")
	fixture.commitAndPush("remote.txt", "remote\n", "remote update")
	wantBaseOID := fixture.output(fixture.seed, "rev-parse", "HEAD")
	if beforeRemoteRef == wantBaseOID {
		t.Fatal("fixture did not create an unfetched remote commit")
	}

	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	plannedAt := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	planner := Planner{
		Git: SystemGit{},
		Locker: FileRepositoryLocker{Set: lockops.Set{
			Root: filepath.Join(t.TempDir(), "control"),
		}},
		Now: func() time.Time { return plannedAt },
	}

	plan, err := planner.Build(context.Background(), Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: t.TempDir(),
		Repositories: map[string]Repository{
			"api": {
				SourcePath:     fixture.source,
				Folder:         "api",
				Remote:         "origin",
				BasePreference: "main",
			},
		},
	}, Request{Name: "EVOVPC-3048", RepositoryIDs: []string{"api"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if plan.WorkName.String() != "EVOVPC-3048" || plan.Mode != ModeOnline || !plan.NoRemoteMutation {
		t.Fatalf("unexpected work-level plan: %+v", plan)
	}
	if len(plan.Repositories) != 1 {
		t.Fatalf("repository plans = %d, want 1", len(plan.Repositories))
	}
	repo := plan.Repositories[0]
	if repo.BaseRef != "refs/remotes/origin/main" || repo.BaseOID != wantBaseOID {
		t.Fatalf("base = %s %s, want refs/remotes/origin/main %s", repo.BaseRef, repo.BaseOID, wantBaseOID)
	}
	if repo.FetchedAt == nil || !repo.FetchedAt.Equal(plannedAt) {
		t.Fatalf("FetchedAt = %v, want %v", repo.FetchedAt, plannedAt)
	}
	if !repo.IncludeInGoWork {
		t.Fatal("root go.mod was not detected in planned commit")
	}
	if _, err := os.Stat(plan.WorkRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planner mutated Work root: err=%v", err)
	}
	if _, exists, err := gitops.LocalBranchOIDContext(context.Background(), fixture.source, "EVOVPC-3048"); err != nil || exists {
		t.Fatalf("planner created branch: exists=%v err=%v", exists, err)
	}
	if got := fixture.output(fixture.source, "rev-parse", "HEAD"); got != beforeSourceHead {
		t.Fatalf("source HEAD changed: got %s, want %s", got, beforeSourceHead)
	}
}

func TestPlannerBuildOfflineDoesNotRequireRemoteOrLock(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, true)
	wantOID := gitOutput(t, repo, "rev-parse", "refs/heads/main")
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)

	plan, err := (Planner{Git: SystemGit{}}).Build(context.Background(), Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: t.TempDir(),
		Repositories: map[string]Repository{
			"service": {SourcePath: repo, Folder: "service", BasePreference: "main"},
		},
	}, Request{Name: "work-1", RepositoryIDs: []string{"service"}, Mode: ModeOffline})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := plan.Repositories[0]
	if got.FetchedAt != nil || got.Remote != "" {
		t.Fatalf("offline plan claims fetch: %+v", got)
	}
	if got.BaseRef != "refs/heads/main" || got.BaseOID != wantOID {
		t.Fatalf("offline base = %s %s, want refs/heads/main %s", got.BaseRef, got.BaseOID, wantOID)
	}
}

func TestPlannerRejectsBranchCollisionBeforeOnlineFetch(t *testing.T) {
	t.Parallel()

	fixture := newRemoteFixture(t, false)
	fixture.run(fixture.source, "branch", "work-1")
	beforeRemoteRef := fixture.output(fixture.source, "rev-parse", "refs/remotes/origin/main")
	fixture.commitAndPush("remote.txt", "remote\n", "remote update")

	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	planner := Planner{
		Git:    SystemGit{},
		Locker: FileRepositoryLocker{Set: lockops.Set{Root: filepath.Join(t.TempDir(), "control")}},
	}
	_, err := planner.Build(context.Background(), Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: t.TempDir(),
		Repositories: map[string]Repository{
			"repo": {SourcePath: fixture.source, Folder: "repo", Remote: "origin", BasePreference: "main"},
		},
	}, Request{Name: "work-1", RepositoryIDs: []string{"repo"}})
	assertProblemCode(t, err, CodeAlreadyExists)
	if got := fixture.output(fixture.source, "rev-parse", "refs/remotes/origin/main"); got != beforeRemoteRef {
		t.Fatalf("preflight collision still fetched: got %s, want %s", got, beforeRemoteRef)
	}
}

func TestPlannerRejectsDuplicateGitIdentity(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	_, err := (Planner{Git: SystemGit{}}).Build(context.Background(), Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: t.TempDir(),
		Repositories: map[string]Repository{
			"one": {SourcePath: repo, Folder: "one", BasePreference: "main"},
			"two": {SourcePath: repo, Folder: "two", BasePreference: "main"},
		},
	}, Request{Name: "work-1", RepositoryIDs: []string{"one", "two"}, Mode: ModeOffline})
	assertProblemCode(t, err, CodeInvalidInput)
}

func TestPlannerAppliesGitBranchValidationWithoutNormalizingName(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	_, err := (Planner{Git: SystemGit{}}).Build(context.Background(), Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: t.TempDir(),
		Repositories: map[string]Repository{
			"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
		},
	}, Request{Name: "work..one", RepositoryIDs: []string{"repo"}, Mode: ModeOffline})
	assertProblemCode(t, err, CodeInvalidInput)
}

func TestPlannerClassifiesCancellation(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Planner{Git: SystemGit{}}).Build(ctx, Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: t.TempDir(),
		Repositories: map[string]Repository{
			"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
		},
	}, Request{Name: "work-1", RepositoryIDs: []string{"repo"}, Mode: ModeOffline})
	assertProblemCode(t, err, CodeInterrupted)
}

func TestPlannerRoutesExistingOperationToResumeBeforeGitPreparation(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	planner := Planner{Git: SystemGit{}}
	catalog := Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: controlRoot,
		Repositories: map[string]Repository{
			"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
		},
	}
	request := Request{Name: "work-existing-op", RepositoryIDs: []string{"repo"}, Mode: ModeOffline}
	plan, err := planner.Build(context.Background(), catalog, request)
	if err != nil {
		t.Fatal(err)
	}
	record := newOperationRecord(plan, strings.Repeat("c", 32), time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC))
	if err := (OperationStore{}).Create(record); err != nil {
		t.Fatal(err)
	}
	_, err = planner.Build(context.Background(), catalog, request)
	assertProblemCode(t, err, CodeAlreadyExists)
}

func TestPlannerBlocksWorkNameUntilActiveRemoveRecordIsFinalized(t *testing.T) {
	t.Parallel()

	repo := newLocalRepository(t, false)
	worksRoot := filepath.Join(t.TempDir(), "works")
	mustMkdir(t, worksRoot)
	controlRoot := t.TempDir()
	worksRoot, err := filepath.EvalSymlinks(worksRoot)
	if err != nil {
		t.Fatal(err)
	}
	controlRoot, err = filepath.EvalSymlinks(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{
		WorksRoot:   worksRoot,
		ControlRoot: controlRoot,
		Repositories: map[string]Repository{
			"repo": {SourcePath: repo, Folder: "repo", BasePreference: "main"},
		},
	}
	request := Request{Name: "work-being-removed", RepositoryIDs: []string{"repo"}, Mode: ModeOffline}
	workName, err := work.ParseName(request.Name)
	if err != nil {
		t.Fatal(err)
	}
	workID := work.NewIdentity(worksRoot, workName)
	removeRecord := filepath.Join(controlRoot, "operations", "remove-work", workID.String()+".json")
	mustMkdir(t, filepath.Dir(removeRecord))
	if err := os.WriteFile(removeRecord, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = (Planner{Git: SystemGit{}}).Build(context.Background(), catalog, request)
	assertProblemCode(t, err, CodeAlreadyExists)
	if err == nil || !strings.Contains(err.Error(), "Resume Remove Work") {
		t.Fatalf("active Remove error = %v, want Resume Remove Work guidance", err)
	}

	if err := os.Remove(removeRecord); err != nil {
		t.Fatal(err)
	}
	if _, err := (Planner{Git: SystemGit{}}).Build(context.Background(), catalog, request); err != nil {
		t.Fatalf("Build after Remove finalization: %v", err)
	}
}

func assertProblemCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", code)
	}
	var problems *ProblemsError
	if !errors.As(err, &problems) {
		t.Fatalf("error type = %T, want *ProblemsError: %v", err, err)
	}
	for _, problem := range problems.Problems {
		if problem.Code == code {
			return
		}
	}
	t.Fatalf("problems = %+v, want code %s", problems.Problems, code)
}

type remoteFixture struct {
	t      *testing.T
	origin string
	seed   string
	source string
}

func newRemoteFixture(t *testing.T, rootModule bool) *remoteFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	fixture := &remoteFixture{
		t:      t,
		origin: filepath.Join(root, "origin.git"),
		seed:   filepath.Join(root, "seed"),
		source: filepath.Join(root, "source"),
	}
	fixture.run(root, "init", "--bare", "--initial-branch=main", fixture.origin)
	fixture.run(root, "clone", fixture.origin, fixture.seed)
	fixture.write(fixture.seed, "README.md", "initial\n")
	if rootModule {
		fixture.write(fixture.seed, "go.mod", "module example.test/repo\n\ngo 1.23\n")
	}
	fixture.run(fixture.seed, "add", ".")
	fixture.run(fixture.seed, "commit", "-m", "initial")
	fixture.run(fixture.seed, "push", "-u", "origin", "main")
	fixture.run(root, "clone", fixture.origin, fixture.source)
	return fixture
}

func (f *remoteFixture) commitAndPush(name, content, message string) {
	f.t.Helper()
	f.write(f.seed, name, content)
	f.run(f.seed, "add", name)
	f.run(f.seed, "commit", "-m", message)
	f.run(f.seed, "push", "origin", "main")
}

func (f *remoteFixture) write(dir, name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *remoteFixture) run(dir string, args ...string) {
	f.t.Helper()
	gitRun(f.t, dir, args...)
}

func (f *remoteFixture) output(dir string, args ...string) string {
	f.t.Helper()
	return gitOutput(f.t, dir, args...)
}

func newLocalRepository(t *testing.T, rootModule bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	mustMkdir(t, repo)
	gitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rootModule {
		if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/repo\n\ngo 1.23\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnvironment()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnvironment()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitTestEnvironment() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=goworktree-test",
		"GIT_AUTHOR_EMAIL=goworktree-test@example.com",
		"GIT_COMMITTER_NAME=goworktree-test",
		"GIT_COMMITTER_EMAIL=goworktree-test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
	)
}
