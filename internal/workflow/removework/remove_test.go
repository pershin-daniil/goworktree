package removework

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
)

func TestExecutorRequiresExactConfirmationBeforeRecordingOrMutation(t *testing.T) {
	plan, git := testPlan(t)
	_, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan, "wrong")
	if err == nil {
		t.Fatal("expected confirmation error")
	}
	if len(git.calls) != 0 {
		t.Fatalf("Git calls = %v", git.calls)
	}
	if _, err := os.Stat(plan.OperationRecord); !os.IsNotExist(err) {
		t.Fatalf("operation record exists before confirmation: %v", err)
	}
}

func TestExecutorRemovesExactWorktreeBranchAndRoot(t *testing.T) {
	plan, git := testPlan(t)
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan, plan.WorkName)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RootRemoved || len(result.Repositories) != 1 || result.Repositories[0].Status != "removed" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Lstat(plan.WorkRoot); !os.IsNotExist(err) {
		t.Fatalf("Work root still exists: %v", err)
	}
	if len(git.calls) != 2 || git.calls[0] != "remove" || git.calls[1] != "delete" {
		t.Fatalf("calls = %v", git.calls)
	}
	if _, err := os.Stat(plan.OperationRecord); err != nil {
		t.Fatalf("external operation record missing: %v", err)
	}
}

func testPlan(t *testing.T) (Plan, *fakeGit) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	destination := filepath.Join(workRoot, "repo")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workRoot, ".goworktree.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		WorkName: "work", WorkID: "id", WorkRoot: workRoot,
		OperationRecord: filepath.Join(root, "control", "remove.json"), Irreversible: true,
		RootEntries: []Entry{{Name: ".goworktree.json", Kind: "file"}, {Name: "repo", Kind: "directory"}},
		Repositories: []RepositoryPlan{{
			ID: "repo", SourcePath: filepath.Join(root, "source"), GitCommonDir: "common", Destination: destination,
			BranchRef: "refs/heads/work", BranchOID: "head",
		}},
	}
	git := &fakeGit{plan: plan.Repositories[0], branchExists: true}
	return plan, git
}

type fakeGit struct {
	plan         RepositoryPlan
	branchExists bool
	calls        []string
}

func (f *fakeGit) InspectCheckout(context.Context, string) (gitops.Checkout, error) {
	return gitops.Checkout{Identity: gitops.RepositoryIdentity{CommonDir: f.plan.GitCommonDir}, FullRef: f.plan.BranchRef, HeadOID: f.plan.BranchOID}, nil
}
func (f *fakeGit) WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error) {
	return f.plan.WorkingTree, nil
}
func (f *fakeGit) ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error) {
	return nil, nil
}
func (f *fakeGit) ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error) {
	return []gitops.WorktreeRegistration{{Path: f.plan.Destination, Branch: f.plan.BranchRef, HeadOID: f.plan.BranchOID}}, nil
}
func (f *fakeGit) LocalBranchOID(context.Context, string, string) (string, bool, error) {
	return f.plan.BranchOID, f.branchExists, nil
}
func (f *fakeGit) RemoveWorktree(_ context.Context, _, destination string) error {
	f.calls = append(f.calls, "remove")
	return os.RemoveAll(destination)
}
func (f *fakeGit) DeleteLocalBranchAtOID(context.Context, string, string, string) error {
	f.calls = append(f.calls, "delete")
	f.branchExists = false
	return nil
}

type fakeLocker struct{}

func (fakeLocker) AcquireExecution(context.Context, string, []string) (func() error, error) {
	return func() error { return nil }, nil
}
