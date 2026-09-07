package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestListWorktreesContextParsesPorcelainZ(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	inspectGitRun(t, repo, "worktree", "add", "-b", "work-1", linked, "main")

	worktrees, err := ListWorktreesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want 2", worktrees)
	}
	canonicalLinked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	if worktrees[1].Path != canonicalLinked || worktrees[1].Branch != "refs/heads/work-1" {
		t.Fatalf("linked worktree = %+v", worktrees[1])
	}
}

func TestResolveNewWorkBaseOverrideIsAuthoritative(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "initial")

	if _, err := ResolveNewWorkBaseContext(context.Background(), repo, "", "missing", "main"); err == nil {
		t.Fatal("missing request override silently fell back to main")
	}
	base, err := ResolveNewWorkBaseContext(context.Background(), repo, "", "", "missing")
	if err != nil {
		t.Fatalf("configured preference fallback: %v", err)
	}
	if base.FullRef != "refs/heads/main" || base.OID == "" {
		t.Fatalf("base = %+v", base)
	}
}

func TestDeleteLocalBranchAtOIDRequiresExactTip(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "initial")
	inspectGitRun(t, repo, "branch", "work")
	oid, exists, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/work")
	if err != nil || !exists {
		t.Fatalf("branch OID = %q, %v, %v", oid, exists, err)
	}
	if err := DeleteLocalBranchAtOIDContext(context.Background(), repo, "refs/heads/work", strings.Repeat("0", 40)); err == nil {
		t.Fatal("branch deleted with a mismatched expected OID")
	}
	if _, exists, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/work"); err != nil || !exists {
		t.Fatalf("branch missing after rejected deletion: %v, %v", exists, err)
	}
	if err := DeleteLocalBranchAtOIDContext(context.Background(), repo, "refs/heads/work", oid); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/work"); err != nil || exists {
		t.Fatalf("branch still exists: %v, %v", exists, err)
	}
}

func TestUpdateLocalBranchAtOIDFastForwardsUncheckedOutBranch(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "initial")
	oldOID, _, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "checkout", "-b", "topic")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "next")
	newOID, _, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/topic")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateLocalBranchAtOIDContext(context.Background(), repo, "refs/heads/main", oldOID, newOID); err != nil {
		t.Fatal(err)
	}
	observed, exists, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/main")
	if err != nil || !exists || observed != newOID {
		t.Fatalf("main = %q, %v, %v", observed, exists, err)
	}
}

func TestFastForwardCheckoutAtOIDRejectsDirtyCheckout(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "initial")
	oldOID, _, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "branch", "next")
	inspectGitRun(t, repo, "checkout", "next")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "next")
	newOID, _, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/next")
	if err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FastForwardCheckoutAtOIDContext(context.Background(), repo, "refs/heads/main", oldOID, newOID); err == nil {
		t.Fatal("dirty checkout was updated")
	}
	observed, _, err := LocalBranchOIDContext(context.Background(), repo, "refs/heads/main")
	if err != nil || observed != oldOID {
		t.Fatalf("main changed to %q: %v", observed, err)
	}
}

func TestPruneAndAttachWorktreeRestoresMissingCheckout(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	linked := filepath.Join(root, "linked")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspectGitRun(t, repo, "add", ".")
	inspectGitRun(t, repo, "commit", "-m", "initial")
	inspectGitRun(t, repo, "worktree", "add", "-b", "work", linked, "main")
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	if err := PruneAndAttachWorktreeContext(context.Background(), repo, linked, "refs/heads/work"); err != nil {
		t.Fatal(err)
	}
	checkout, err := InspectCheckoutContext(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if checkout.FullRef != "refs/heads/work" {
		t.Fatalf("checkout = %+v", checkout)
	}
}

func inspectGitRun(t *testing.T, dir string, args ...string) {
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
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
