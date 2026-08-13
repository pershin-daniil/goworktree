package syncwork

import (
	"context"
	"errors"
	"testing"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
)

func TestPlannerFetchesAndRecordsExactBaseOID(t *testing.T) {
	git := &fakeGit{base: gitops.ResolvedBase{FullRef: "refs/remotes/upstream/main", OID: "base"}}
	snapshot := healthySnapshot(t, "b", "head")
	plan, err := (Planner{Git: git, Now: func() time.Time { return time.Unix(42, 0) }}).Build(
		context.Background(), snapshot, []RepositoryConfig{{ID: "b", Remote: "upstream", BasePreference: "main"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repos) != 1 || plan.Repos[0].BaseOID != "base" || plan.Repos[0].Relation != RelationDiverged {
		t.Fatalf("plan = %+v", plan)
	}
	if git.fetched != "source:upstream" {
		t.Fatalf("fetch = %q", git.fetched)
	}
}

func TestExecutorContinuesAfterRepositoryFailure(t *testing.T) {
	git := &fakeGit{checkouts: map[string]gitops.Checkout{
		"a": {Identity: gitops.RepositoryIdentity{CommonDir: "common-a"}, FullRef: "refs/heads/work", HeadOID: "head-a"},
		"b": {Identity: gitops.RepositoryIdentity{CommonDir: "common-b"}, FullRef: "refs/heads/work", HeadOID: "head-b"},
	}, rebase: map[string]gitops.SyncResult{
		"a": {Status: gitops.SyncConflict, Err: errors.New("conflict")},
		"b": {Status: gitops.SyncRebased, From: "head-b", To: "new-b"},
	}}
	plan := Plan{WorkName: "work", WorkID: "id", Repos: []RepositoryPlan{
		{ID: "a", Destination: "a", GitCommonDir: "common-a", BranchRef: "refs/heads/work", PreHeadOID: "head-a", BaseOID: "base-a", Relation: RelationDiverged},
		{ID: "b", Destination: "b", GitCommonDir: "common-b", BranchRef: "refs/heads/work", PreHeadOID: "head-b", BaseOID: "base-b", Relation: RelationDiverged},
	}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].Status != gitops.SyncConflict || result.Repositories[1].Status != gitops.SyncRebased {
		t.Fatalf("result = %+v", result)
	}
	if len(git.rebased) != 2 || git.rebased[1] != "b:base-b" {
		t.Fatalf("rebases = %v", git.rebased)
	}
}

func TestExecutorRejectsChangedHeadWithoutMutation(t *testing.T) {
	git := &fakeGit{checkouts: map[string]gitops.Checkout{
		"repo": {Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "changed"},
	}}
	plan := Plan{WorkName: "work", WorkID: "id", Repos: []RepositoryPlan{{
		ID: "repo", Destination: "repo", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "planned", BaseOID: "base", Relation: RelationDiverged,
	}}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(git.rebased) != 0 || result.Repositories[0].Err == nil {
		t.Fatalf("result = %+v; rebases = %v", result, git.rebased)
	}
}

func healthySnapshot(t *testing.T, id, head string) inspectwork.Snapshot {
	t.Helper()
	name, err := work.ParseName("work")
	if err != nil {
		t.Fatal(err)
	}
	intent := work.RepositoryIntent{ID: id, SourcePath: "source", GitCommonDir: "common", BaseRef: "main", BaseOID: "old", BranchRef: "refs/heads/work", Destination: "destination"}
	manifest := &work.Manifest{Repositories: []work.RepositoryIntent{intent}}
	return inspectwork.Snapshot{
		WorkName: name, WorkID: work.NewIdentity("/works", name), WorkRoot: "/works/work",
		Manifest: inspectwork.ManifestSnapshot{State: inspectwork.MetadataValid, Value: manifest},
		Repositories: []inspectwork.RepositorySnapshot{{
			ID: id, Intent: intent, SourceKnown: true, CheckoutKnown: true, WorkingTreeKnown: true, GitOperationsKnown: true,
			Checkout: inspectwork.CheckoutSnapshot{HeadOID: head, FullRef: intent.BranchRef},
		}},
	}
}

type fakeGit struct {
	base      gitops.ResolvedBase
	fetched   string
	checkouts map[string]gitops.Checkout
	rebase    map[string]gitops.SyncResult
	rebased   []string
}

func (f *fakeGit) FetchRemote(_ context.Context, repo, remote string) error {
	f.fetched = repo + ":" + remote
	return nil
}
func (f *fakeGit) ResolveBase(context.Context, string, string, string) (gitops.ResolvedBase, error) {
	return f.base, nil
}
func (f *fakeGit) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (f *fakeGit) InspectCheckout(_ context.Context, path string) (gitops.Checkout, error) {
	return f.checkouts[path], nil
}
func (f *fakeGit) WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error) {
	return gitops.WorkingTreeStatus{}, nil
}
func (f *fakeGit) ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error) {
	return nil, nil
}
func (f *fakeGit) RebaseToOID(_ context.Context, path, _ string, oid string) gitops.SyncResult {
	f.rebased = append(f.rebased, path+":"+oid)
	return f.rebase[path]
}

type fakeLocker struct{}

func (fakeLocker) AcquireExecution(context.Context, string, []string) (func() error, error) {
	return func() error { return nil }, nil
}
