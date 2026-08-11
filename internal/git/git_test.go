package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddProjectWorktreeFromCheckedOutBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run(repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "README")
	run(repo, "commit", "-m", "init")

	// main is checked out in primary clone — creating worktree on same branch must fail historically;
	// AddProjectWorktree creates a new branch from main.
	dest := filepath.Join(root, "wt-feature")
	if err := AddProjectWorktree(repo, dest, "feature-x", "main"); err != nil {
		t.Fatalf("AddProjectWorktree: %v", err)
	}

	branch, err := CurrentBranch(dest)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature-x" {
		t.Fatalf("worktree branch = %q", branch)
	}

	// main still checked out in primary
	mainBranch, err := CurrentBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	if mainBranch != "main" {
		t.Fatalf("primary branch = %q", mainBranch)
	}
}

func TestResolveBaseBranchFallsBackToDetected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run(repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "README")
	run(repo, "commit", "-m", "init")

	base, err := ResolveBaseBranch(repo, "develop")
	if err != nil {
		t.Fatalf("ResolveBaseBranch: %v", err)
	}
	if base != "main" {
		t.Fatalf("expected main, got %q", base)
	}

	base, err = ResolveBaseBranch(repo, "main")
	if err != nil || base != "main" {
		t.Fatalf("preferred main: got %q err=%v", base, err)
	}

	if !SameBranchRef("main", "origin/main") {
		t.Fatal("SameBranchRef main/origin/main")
	}
	if SameBranchRef("develop", "main") {
		t.Fatal("SameBranchRef develop/main should be false")
	}
}

func TestDefaultBranchPrefersMain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(repo, 0o755)

	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f"}, {"commit", "-m", "c"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	got, err := DefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got != "main" {
		t.Fatalf("DefaultBranch = %q", got)
	}
}

func TestSyncWorktreeRebasesAndRestoresDirtyFiles(t *testing.T) {
	origin, seed, work := setupSyncRepos(t)
	_ = origin

	writeCommitPush(t, seed, "remote.txt", "remote\n", "remote update")
	if err := os.WriteFile(filepath.Join(work, "local.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, work, "add", "local.txt")
	if err := os.WriteFile(filepath.Join(work, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := SyncWorktree(work, "ticket", "main")
	if result.Status != SyncRebased || result.Err != nil {
		t.Fatalf("SyncWorktree = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(work, "remote.txt")); err != nil {
		t.Fatal("remote update missing:", err)
	}
	status := gitTestOutput(t, work, "status", "--porcelain")
	if !strings.Contains(status, "A  local.txt") || !strings.Contains(status, "?? untracked.txt") {
		t.Fatalf("dirty state not restored:\n%s", status)
	}
}

func TestSyncWorktreeRebasesDivergedBranch(t *testing.T) {
	_, seed, work := setupSyncRepos(t)
	writeCommitPush(t, seed, "remote.txt", "remote\n", "remote update")
	if err := os.WriteFile(filepath.Join(work, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, work, "add", "local.txt")
	gitTestRun(t, work, "commit", "-m", "local commit")
	before := strings.TrimSpace(gitTestOutput(t, work, "rev-parse", "HEAD"))

	result := SyncWorktree(work, "ticket", "main")
	if result.Status != SyncRebased || result.Err != nil {
		t.Fatalf("SyncWorktree = %+v", result)
	}
	after := strings.TrimSpace(gitTestOutput(t, work, "rev-parse", "HEAD"))
	if after == before {
		t.Fatalf("rebase did not rewrite local commit: %s", before)
	}
	gitTestRun(t, work, "merge-base", "--is-ancestor", "origin/main", "HEAD")
	if data, err := os.ReadFile(filepath.Join(work, "local.txt")); err != nil || string(data) != "local\n" {
		t.Fatalf("rebased local commit missing: %q err=%v", data, err)
	}
}

func TestSyncWorktreeLeavesConflictingRebaseForResolutionAndRetries(t *testing.T) {
	_, seed, work := setupSyncRepos(t)
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte("ticket change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, work, "add", "shared.txt")
	gitTestRun(t, work, "commit", "-m", "ticket change")
	writeCommitPush(t, seed, "shared.txt", "base change\n", "base change")

	result := SyncWorktree(work, "ticket", "main")
	if result.Status != SyncConflict || result.Err == nil {
		t.Fatalf("SyncWorktree = %+v", result)
	}
	if !strings.Contains(result.Err.Error(), "resolve") {
		t.Fatalf("conflict error lacks resolution guidance: %v", result.Err)
	}
	if !rebaseInProgress(work) {
		t.Fatal("rebase state was not retained for manual resolution")
	}
	gitTestRun(t, work, "add", "shared.txt")
	result = SyncWorktree(work, "ticket", "main")
	if result.Status != SyncRebased || result.Err != nil {
		t.Fatalf("SyncWorktree retry = %+v", result)
	}
}

func TestSyncWorktreeRollsBackWhenStashConflicts(t *testing.T) {
	_, seed, work := setupSyncRepos(t)
	writeCommitPush(t, seed, "shared.txt", "remote change\n", "remote update")
	before := strings.TrimSpace(gitTestOutput(t, work, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(work, "shared.txt"), []byte("local change\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := SyncWorktree(work, "ticket", "origin/main")
	if result.Status != SyncRolledBack || result.Err == nil {
		t.Fatalf("SyncWorktree = %+v", result)
	}
	after := strings.TrimSpace(gitTestOutput(t, work, "rev-parse", "HEAD"))
	if after != before {
		t.Fatalf("HEAD changed after rollback: %s -> %s", before, after)
	}
	data, err := os.ReadFile(filepath.Join(work, "shared.txt"))
	if err != nil || string(data) != "local change\n" {
		t.Fatalf("local change not restored: %q err=%v", data, err)
	}
}

func TestSyncWorktreeRejectsUnexpectedAndDetachedBranches(t *testing.T) {
	_, _, work := setupSyncRepos(t)
	result := SyncWorktree(work, "another-ticket", "main")
	if result.Status != SyncFailed || result.Err == nil || !strings.Contains(result.Err.Error(), "expected") {
		t.Fatalf("unexpected branch result = %+v", result)
	}

	gitTestRun(t, work, "checkout", "--detach")
	result = SyncWorktree(work, "ticket", "main")
	if result.Status != SyncFailed || result.Err == nil || !strings.Contains(result.Err.Error(), "detached") {
		t.Fatalf("detached result = %+v", result)
	}
}

func TestSyncWorktreeReportsMissingRemoteBase(t *testing.T) {
	_, _, work := setupSyncRepos(t)
	result := SyncWorktree(work, "ticket", "missing")
	if result.Status != SyncFailed || result.Err == nil || !strings.Contains(result.Err.Error(), "origin/missing") {
		t.Fatalf("missing base result = %+v", result)
	}
}

func setupSyncRepos(t *testing.T) (origin, seed, work string) {
	t.Helper()
	root := t.TempDir()
	origin = filepath.Join(root, "origin.git")
	seed = filepath.Join(root, "seed")
	work = filepath.Join(root, "work")
	gitTestRun(t, root, "init", "--bare", origin)
	gitTestRun(t, root, "clone", origin, seed)
	gitTestRun(t, seed, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "shared.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, seed, "add", "shared.txt")
	gitTestRun(t, seed, "commit", "-m", "initial")
	gitTestRun(t, seed, "push", "-u", "origin", "main")
	gitTestRun(t, root, "clone", "--branch", "main", origin, work)
	gitTestRun(t, work, "checkout", "-b", "ticket")
	return origin, seed, work
}

func writeCommitPush(t *testing.T, repo, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "add", name)
	gitTestRun(t, repo, "commit", "-m", message)
	gitTestRun(t, repo, "push", "origin", "main")
}

func gitTestRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
