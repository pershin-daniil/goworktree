package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveDeletesManifestAndUnlistedWorktrees(t *testing.T) {
	repo := initRepo(t)
	projectDir := filepath.Join(t.TempDir(), "mistake")
	listed := filepath.Join(projectDir, "listed")
	unlisted := filepath.Join(projectDir, "unlisted")
	gitRun(t, repo, "worktree", "add", "-b", "listed-branch", listed, "main")
	gitRun(t, repo, "worktree", "add", "-b", "unlisted-branch", unlisted, "main")

	m := NewManifest("mistake", []ManifestRepo{{
		ID: "repo", Folder: "listed", Path: repo, Branch: "listed-branch", Status: StatusReady,
	}})
	if err := m.Save(projectDir); err != nil {
		t.Fatal(err)
	}

	entry := loadEntry("mistake", projectDir)
	if len(entry.Worktrees) != 2 {
		t.Fatalf("found %d worktrees, want 2", len(entry.Worktrees))
	}
	if err := Remove(entry, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("project directory remains: %v", err)
	}
	for _, branch := range []string{"listed-branch", "unlisted-branch"} {
		if output := gitOutput(t, repo, "branch", "--list", branch); strings.TrimSpace(output) != "" {
			t.Fatalf("branch %q remains: %s", branch, output)
		}
	}
	if output := gitOutput(t, repo, "worktree", "list", "--porcelain"); strings.Contains(output, projectDir) {
		t.Fatalf("stale worktree registration remains:\n%s", output)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README")
	gitRun(t, repo, "commit", "-m", "init")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
