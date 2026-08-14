package git

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPreserveWorktreeMovesOnlyOwnedStashToRecoveryRef(t *testing.T) {
	_, _, repo := setupSyncRepos(t)
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("user stash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "stash", "push", "--message", "user-owned")
	baseline, err := StashEntriesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const recoveryRef = "refs/goworktree/recovery/work/op/repo"
	const marker = "goworktree-sync:work:op:repo"
	oid, err := PreserveWorktreeContext(context.Background(), repo, recoveryRef, marker, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if oid == "" {
		t.Fatal("empty recovery OID")
	}
	current, err := StashEntriesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, baseline) {
		t.Fatalf("user stash stack changed:\n got %+v\nwant %+v", current, baseline)
	}
	if stored, exists, err := RefOIDContext(context.Background(), repo, recoveryRef); err != nil || !exists || stored != oid {
		t.Fatalf("recovery ref = %q, %v, %v", stored, exists, err)
	}
	if status := gitTestOutput(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("preserved worktree is not clean:\n%s", status)
	}
	if err := ApplyRecoveryContext(context.Background(), repo, oid); err != nil {
		t.Fatal(err)
	}
	status := gitTestOutput(t, repo, "status", "--porcelain")
	if status == "" {
		t.Fatal("recovery did not restore changes")
	}
	if err := DeleteRecoveryRefContext(context.Background(), repo, recoveryRef, oid); err != nil {
		t.Fatal(err)
	}
}

func TestPreserveWorktreeRecoversCrashAfterStashPush(t *testing.T) {
	_, _, repo := setupSyncRepos(t)
	baseline, err := StashEntriesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "crash.txt"), []byte("recover me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const recoveryRef = "refs/goworktree/recovery/work/op/crash"
	const marker = "goworktree-sync:work:op:crash"
	gitTestRun(t, repo, "stash", "push", "--include-untracked", "--message", marker)

	oid, err := PreserveWorktreeContext(context.Background(), repo, recoveryRef, marker, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if oid == "" {
		t.Fatal("empty recovered OID")
	}
	current, err := StashEntriesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current, baseline) {
		t.Fatalf("stash stack after recovery = %+v, want %+v", current, baseline)
	}
	if _, err := os.Stat(filepath.Join(repo, "crash.txt")); !os.IsNotExist(err) {
		t.Fatalf("preserved untracked file unexpectedly present: %v", err)
	}
}

func TestRemoveWorktreePrunesRegistrationWhenDirectoryIsAlreadyMissing(t *testing.T) {
	_, _, repo := setupSyncRepos(t)
	destination := filepath.Join(filepath.Dir(repo), "broken-worktree")
	gitTestRun(t, repo, "worktree", "add", "-b", "remove-me", destination, "main")
	if err := os.RemoveAll(destination); err != nil {
		t.Fatal(err)
	}
	// `git worktree remove` reports the missing path, but the same operation
	// still prunes its stale registration. The caller reconciles observed state.
	_ = RemoveWorktreeContext(context.Background(), repo, destination)
	registrations, err := ListWorktreesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range registrations {
		if filepath.Clean(registration.Path) == filepath.Clean(destination) {
			t.Fatalf("stale registration remains: %+v", registration)
		}
	}
}
