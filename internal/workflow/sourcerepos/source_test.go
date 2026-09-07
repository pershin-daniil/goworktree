package sourcerepos

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
)

func TestInspectorReportsLocallyKnownRelation(t *testing.T) {
	git := healthyFakeGit("repo", "old", "new")
	snapshot := (Inspector{Git: git, Now: fixedTime}).Inspect(context.Background(), []RepositoryConfig{{
		ID: "api", Name: "API", Path: "repo", Remote: "origin", DefaultBranch: "main", Groups: []string{"team-a"},
	}})
	if len(snapshot.Repositories) != 1 {
		t.Fatalf("repositories = %+v", snapshot.Repositories)
	}
	observed := snapshot.Repositories[0]
	if observed.Relation != RelationBehind || !observed.LocalKnown || !observed.RemoteKnown || !reflect.DeepEqual(observed.Groups, []string{"team-a"}) {
		t.Fatalf("snapshot = %+v", observed)
	}
	if !observed.FetchedAt.IsZero() {
		t.Fatalf("local inspection has fetched timestamp %v", observed.FetchedAt)
	}
}

func TestPlannerFetchesAndPlansCheckedOutFastForward(t *testing.T) {
	git := healthyFakeGit("repo", "old", "new")
	plan, err := (Planner{Git: git, Locker: fakeLocker{}, Now: fixedTime}).Build(context.Background(), []RepositoryConfig{{
		ID: "api", Path: "repo", Remote: "origin", DefaultBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 1 || plan.Repositories[0].Action != ActionUpdateCheckout || plan.Repositories[0].TargetOID != "new" {
		t.Fatalf("plan = %+v", plan)
	}
	if !reflect.DeepEqual(git.fetched, []string{"repo:origin"}) {
		t.Fatalf("fetches = %v", git.fetched)
	}
}

func TestPlannerSkipsDirtyCheckedOutDefault(t *testing.T) {
	git := healthyFakeGit("repo", "old", "new")
	git.statuses["repo"] = gitops.WorkingTreeStatus{Untracked: 1}
	plan, err := (Planner{Git: git, Locker: fakeLocker{}}).Build(context.Background(), []RepositoryConfig{{
		ID: "api", Path: "repo", Remote: "origin", DefaultBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Repositories[0].Action != ActionSkip || plan.Repositories[0].Reason != "source working tree has local changes" {
		t.Fatalf("plan = %+v", plan.Repositories[0])
	}
}

func TestPlannerPlansDirectRefUpdateWhenDefaultIsNotCheckedOut(t *testing.T) {
	git := healthyFakeGit("repo", "old", "new")
	git.checkouts["repo"] = gitops.Checkout{
		Identity: gitops.RepositoryIdentity{SourcePath: "repo", CommonDir: "common-repo"},
		FullRef:  "refs/heads/topic", HeadOID: "topic",
	}
	git.worktrees["repo"] = []gitops.WorktreeRegistration{{Path: "repo", Branch: "refs/heads/topic", HeadOID: "topic"}}
	plan, err := (Planner{Git: git, Locker: fakeLocker{}}).Build(context.Background(), []RepositoryConfig{{
		ID: "api", Path: "repo", Remote: "origin", DefaultBranch: "main",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Repositories[0].Action != ActionUpdateRef {
		t.Fatalf("plan = %+v", plan.Repositories[0])
	}
}

func TestExecutorRevalidatesAndContinuesAfterChangedRepository(t *testing.T) {
	git := healthyFakeGit("a", "changed", "new-a")
	addHealthyRepository(git, "b", "old-b", "new-b")
	plan := Plan{Repositories: []RepositoryPlan{
		{
			RepositoryConfig: RepositoryConfig{ID: "a", Path: "a", Remote: "origin", DefaultBranch: "main"},
			GitCommonDir:     "common-a", LocalRef: "refs/heads/main", LocalOID: "old-a", TargetOID: "new-a", Relation: RelationBehind, Action: ActionUpdateCheckout, CheckoutPath: "a",
		},
		{
			RepositoryConfig: RepositoryConfig{ID: "b", Path: "b", Remote: "origin", DefaultBranch: "main"},
			GitCommonDir:     "common-b", LocalRef: "refs/heads/main", LocalOID: "old-b", TargetOID: "new-b", Relation: RelationBehind, Action: ActionUpdateCheckout, CheckoutPath: "b",
		},
	}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].Status != StatusFailed || result.Repositories[1].Status != StatusUpdated {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(git.fastForwarded, []string{"b:old-b:new-b"}) {
		t.Fatalf("fast-forwards = %v", git.fastForwarded)
	}
}

func TestFetcherPreservesPerRepositoryFailure(t *testing.T) {
	git := healthyFakeGit("a", "old-a", "new-a")
	addHealthyRepository(git, "b", "old-b", "new-b")
	git.fetchErrors["a"] = errors.New("offline")
	result, err := (Fetcher{Git: git, Locker: fakeLocker{}, Now: fixedTime}).Fetch(context.Background(), []RepositoryConfig{
		{ID: "b", Path: "b", Remote: "origin", DefaultBranch: "main"},
		{ID: "a", Path: "a", Remote: "origin", DefaultBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].ID != "a" || result.Repositories[0].Status != StatusFailed || result.Repositories[1].Status != StatusFetched {
		t.Fatalf("result = %+v", result)
	}
	if result.Repositories[1].Snapshot.FetchedAt.IsZero() {
		t.Fatal("successful fetch has no timestamp")
	}
}

func TestFetcherRunsRepositoriesWithBoundedParallelism(t *testing.T) {
	base := healthyFakeGit("a", "old-a", "new-a")
	addHealthyRepository(base, "b", "old-b", "new-b")
	addHealthyRepository(base, "c", "old-c", "new-c")
	addHealthyRepository(base, "d", "old-d", "new-d")
	git := &blockingFetchGit{
		fakeGit: base,
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := (Fetcher{Git: git, Locker: fakeLocker{}, Parallelism: 2}).Fetch(context.Background(), []RepositoryConfig{
			{ID: "d", Path: "d", Remote: "origin", DefaultBranch: "main"},
			{ID: "c", Path: "c", Remote: "origin", DefaultBranch: "main"},
			{ID: "b", Path: "b", Remote: "origin", DefaultBranch: "main"},
			{ID: "a", Path: "a", Remote: "origin", DefaultBranch: "main"},
		})
		done <- outcome{result: result, err: err}
	}()
	for range 2 {
		select {
		case <-git.started:
		case <-time.After(2 * time.Second):
			t.Fatal("fetches did not start concurrently")
		}
	}
	git.mu.Lock()
	maximum := git.maximum
	git.mu.Unlock()
	if maximum != 2 {
		t.Fatalf("maximum concurrent fetches = %d, want 2", maximum)
	}
	close(git.release)
	select {
	case completed := <-done:
		if completed.err != nil {
			t.Fatal(completed.err)
		}
		if got := []string{
			completed.result.Repositories[0].ID,
			completed.result.Repositories[1].ID,
			completed.result.Repositories[2].ID,
			completed.result.Repositories[3].ID,
		}; !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
			t.Fatalf("result order = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parallel fetch did not finish")
	}
}

func TestSystemPlannerAndExecutorFastForwardSourceCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	source := filepath.Join(root, "source")
	sourceGitRun(t, root, "init", "--bare", origin)
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceGitRun(t, seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceGitRun(t, seed, "add", ".")
	sourceGitRun(t, seed, "commit", "-m", "initial")
	sourceGitRun(t, seed, "remote", "add", "origin", origin)
	sourceGitRun(t, seed, "push", "-u", "origin", "main")
	sourceGitRun(t, root, "--git-dir="+origin, "symbolic-ref", "HEAD", "refs/heads/main")
	sourceGitRun(t, root, "clone", origin, source)
	oldOID := strings.TrimSpace(sourceGitOutput(t, source, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceGitRun(t, seed, "add", ".")
	sourceGitRun(t, seed, "commit", "-m", "next")
	sourceGitRun(t, seed, "push", "origin", "main")
	targetOID := strings.TrimSpace(sourceGitOutput(t, seed, "rev-parse", "HEAD"))

	controlRoot := filepath.Join(root, "control")
	if err := os.MkdirAll(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	locker := FileLocker{Set: lockops.Set{Root: controlRoot}}
	config := RepositoryConfig{ID: "source", Path: source, Remote: "origin", DefaultBranch: "main"}
	plan, err := (Planner{Git: SystemGit{}, Locker: locker}).Build(context.Background(), []RepositoryConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 1 || plan.Repositories[0].Action != ActionUpdateCheckout || plan.Repositories[0].LocalOID != oldOID || plan.Repositories[0].TargetOID != targetOID {
		t.Fatalf("plan = %+v", plan)
	}
	result, err := (Executor{Git: SystemGit{}, Locker: locker}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Status != StatusUpdated {
		t.Fatalf("result = %+v", result)
	}
	if observed := strings.TrimSpace(sourceGitOutput(t, source, "rev-parse", "HEAD")); observed != targetOID {
		t.Fatalf("source HEAD = %s, want %s", observed, targetOID)
	}
}

type fakeLocker struct{ err error }

func (l fakeLocker) Acquire(context.Context, []string) (func() error, error) {
	if l.err != nil {
		return nil, l.err
	}
	return func() error { return nil }, nil
}

type fakeGit struct {
	fetchMu       sync.Mutex
	identities    map[string]gitops.RepositoryIdentity
	checkouts     map[string]gitops.Checkout
	statuses      map[string]gitops.WorkingTreeStatus
	operations    map[string][]gitops.ActiveOperation
	worktrees     map[string][]gitops.WorktreeRegistration
	refs          map[string]string
	ancestors     map[string]bool
	fetchErrors   map[string]error
	fetched       []string
	updated       []string
	fastForwarded []string
}

func healthyFakeGit(path, local, remote string) *fakeGit {
	git := &fakeGit{
		identities:  make(map[string]gitops.RepositoryIdentity),
		checkouts:   make(map[string]gitops.Checkout),
		statuses:    make(map[string]gitops.WorkingTreeStatus),
		operations:  make(map[string][]gitops.ActiveOperation),
		worktrees:   make(map[string][]gitops.WorktreeRegistration),
		refs:        make(map[string]string),
		ancestors:   make(map[string]bool),
		fetchErrors: make(map[string]error),
	}
	addHealthyRepository(git, path, local, remote)
	return git
}

func addHealthyRepository(git *fakeGit, path, local, remote string) {
	identity := gitops.RepositoryIdentity{SourcePath: path, CommonDir: "common-" + path}
	git.identities[path] = identity
	git.checkouts[path] = gitops.Checkout{Identity: identity, FullRef: "refs/heads/main", HeadOID: local}
	git.worktrees[path] = []gitops.WorktreeRegistration{{Path: path, Branch: "refs/heads/main", HeadOID: local}}
	git.refs[path+":refs/heads/main"] = local
	git.refs[path+":refs/remotes/origin/main"] = remote
	git.ancestors[path+":"+local+":"+remote] = true
}

func (g *fakeGit) InspectRepository(_ context.Context, path string) (gitops.RepositoryIdentity, error) {
	identity, ok := g.identities[path]
	if !ok {
		return gitops.RepositoryIdentity{}, errors.New("missing repository")
	}
	return identity, nil
}

func (g *fakeGit) InspectCheckout(_ context.Context, path string) (gitops.Checkout, error) {
	checkout, ok := g.checkouts[path]
	if !ok {
		return gitops.Checkout{}, errors.New("missing checkout")
	}
	return checkout, nil
}

func (g *fakeGit) WorkingTreeStatus(_ context.Context, path string) (gitops.WorkingTreeStatus, error) {
	return g.statuses[path], nil
}

func (g *fakeGit) ActiveOperations(_ context.Context, path string) ([]gitops.ActiveOperation, error) {
	return append([]gitops.ActiveOperation(nil), g.operations[path]...), nil
}

func (g *fakeGit) ListWorktrees(_ context.Context, path string) ([]gitops.WorktreeRegistration, error) {
	return append([]gitops.WorktreeRegistration(nil), g.worktrees[path]...), nil
}

func (g *fakeGit) RefOID(_ context.Context, path, ref string) (string, bool, error) {
	oid, ok := g.refs[path+":"+ref]
	return oid, ok, nil
}

func (g *fakeGit) IsAncestor(_ context.Context, path, older, newer string) (bool, error) {
	return g.ancestors[path+":"+older+":"+newer], nil
}

func (g *fakeGit) FetchRemote(_ context.Context, path, remote string) error {
	g.fetchMu.Lock()
	defer g.fetchMu.Unlock()
	g.fetched = append(g.fetched, path+":"+remote)
	return g.fetchErrors[path]
}

type blockingFetchGit struct {
	*fakeGit
	mu      sync.Mutex
	active  int
	maximum int
	started chan struct{}
	release chan struct{}
}

func (g *blockingFetchGit) FetchRemote(ctx context.Context, _, _ string) error {
	g.mu.Lock()
	g.active++
	if g.active > g.maximum {
		g.maximum = g.active
	}
	g.mu.Unlock()
	g.started <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		g.mu.Lock()
		g.active--
		g.mu.Unlock()
		return ctx.Err()
	}
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
	return nil
}

func (g *fakeGit) CommitExists(_ context.Context, path, oid string) error {
	for key, value := range g.refs {
		if value == oid && len(key) >= len(path)+1 && key[:len(path)+1] == path+":" {
			return nil
		}
	}
	return errors.New("missing commit")
}

func (g *fakeGit) UpdateLocalBranch(_ context.Context, path, ref, oldOID, newOID string) error {
	key := path + ":" + ref
	if g.refs[key] != oldOID {
		return errors.New("changed ref")
	}
	g.refs[key] = newOID
	g.updated = append(g.updated, path+":"+oldOID+":"+newOID)
	return nil
}

func (g *fakeGit) FastForwardCheckout(_ context.Context, path, ref, oldOID, newOID string) error {
	key := path + ":" + ref
	if g.refs[key] != oldOID {
		return errors.New("changed ref")
	}
	g.refs[key] = newOID
	checkout := g.checkouts[path]
	checkout.HeadOID = newOID
	g.checkouts[path] = checkout
	g.fastForwarded = append(g.fastForwarded, path+":"+oldOID+":"+newOID)
	return nil
}

func fixedTime() time.Time { return time.Unix(42, 0) }

func sourceGitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = sourceGitOutput(t, dir, args...)
}

func sourceGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=goworktree-test",
		"GIT_AUTHOR_EMAIL=goworktree-test@example.com",
		"GIT_COMMITTER_NAME=goworktree-test",
		"GIT_COMMITTER_EMAIL=goworktree-test@example.com",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
