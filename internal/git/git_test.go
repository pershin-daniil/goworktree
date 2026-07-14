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

