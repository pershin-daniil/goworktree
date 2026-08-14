package repairwork

import (
	"context"
	"errors"
	"testing"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func TestPlannerBuildsOnlyDeterministicRepairs(t *testing.T) {
	name, _ := work.ParseName("work")
	manifest := &work.Manifest{}
	snapshot := inspectwork.Snapshot{
		WorkName: name, WorkID: "id", WorkRoot: "/works/work",
		Manifest:  inspectwork.ManifestSnapshot{State: inspectwork.MetadataValid, Value: manifest},
		Operation: inspectwork.OperationSnapshot{State: inspectwork.MetadataAbsent, Path: "/control/op.json"},
		Harness:   inspectwork.HarnessSnapshot{Kind: inspectwork.PathMissing},
		Problems: []inspectwork.Problem{
			{Code: inspectwork.ProblemOperationMissing}, {Code: inspectwork.ProblemHarnessMissing},
		},
	}
	plan, err := (Planner{}).Build(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 2 || plan.Actions[0].Kind != ActionRegenerateHarness || plan.Actions[1].Kind != ActionRestoreOperation {
		t.Fatalf("actions = %+v", plan.Actions)
	}
}

func TestPlannerRejectsAmbiguousBranchMismatch(t *testing.T) {
	name, _ := work.ParseName("work")
	manifest := &work.Manifest{}
	problem := inspectwork.Problem{Code: inspectwork.ProblemBranchRefMismatch, RepositoryID: "repo", Message: "wrong branch"}
	snapshot := inspectwork.Snapshot{
		WorkName: name, WorkID: "id", Manifest: inspectwork.ManifestSnapshot{State: inspectwork.MetadataValid, Value: manifest},
		Repositories: []inspectwork.RepositorySnapshot{{ID: "repo", Problems: []inspectwork.Problem{problem}}},
		Problems:     []inspectwork.Problem{problem},
	}
	if _, err := (Planner{}).Build(snapshot); err == nil {
		t.Fatal("ambiguous branch mismatch was accepted")
	}
}

func TestExecutorRevalidatesAndRepairsRegistration(t *testing.T) {
	action := Action{
		Kind: ActionRepairRegistration, RepositoryID: "repo", SourcePath: "source", GitCommonDir: "common",
		Destination: "destination", BranchRef: "refs/heads/work", BranchOID: "head",
	}
	git := &fakeGit{action: action}
	result, err := (Executor{Git: git, Locker: fakeLocker{}, Store: newwork.OperationStore{}}).Execute(
		context.Background(), Plan{WorkName: "work", WorkID: "id", Actions: []Action{action}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !git.repaired || len(result.Actions) != 1 || result.Actions[0].Status != "repaired" {
		t.Fatalf("result = %+v, repaired=%v", result, git.repaired)
	}
}

type fakeGit struct {
	action   Action
	repaired bool
}

func (f *fakeGit) LocalBranchOID(context.Context, string, string) (string, bool, error) {
	return f.action.BranchOID, true, nil
}
func (f *fakeGit) ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error) {
	if !f.repaired {
		return nil, nil
	}
	return []gitops.WorktreeRegistration{{Path: f.action.Destination, Branch: f.action.BranchRef, HeadOID: f.action.BranchOID}}, nil
}
func (f *fakeGit) InspectCheckout(context.Context, string) (gitops.Checkout, error) {
	return gitops.Checkout{Identity: gitops.RepositoryIdentity{CommonDir: f.action.GitCommonDir}, FullRef: f.action.BranchRef, HeadOID: f.action.BranchOID}, nil
}
func (f *fakeGit) PruneAndAttach(context.Context, string, string, string) error {
	return errors.New("unexpected")
}
func (f *fakeGit) RepairRegistration(context.Context, string, string) error {
	f.repaired = true
	return nil
}

type fakeLocker struct{}

func (fakeLocker) AcquireExecution(context.Context, string, []string) (func() error, error) {
	return func() error { return nil }, nil
}
