package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreeFingerprintDistinguishesSameStatusCounts(t *testing.T) {
	repoA := fingerprintTestRepo(t)
	repoB := fingerprintTestRepo(t)

	if err := os.WriteFile(filepath.Join(repoA, "alpha.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoB, "beta.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	statusA, err := WorkingTreeStatusContext(context.Background(), repoA)
	if err != nil {
		t.Fatal(err)
	}
	statusB, err := WorkingTreeStatusContext(context.Background(), repoB)
	if err != nil {
		t.Fatal(err)
	}
	if statusA != statusB {
		t.Fatalf("status counts differ: %+v != %+v", statusA, statusB)
	}

	fingerprintA := fingerprintTestValue(t, repoA)
	fingerprintB := fingerprintTestValue(t, repoB)
	if fingerprintA == fingerprintB {
		t.Fatalf("different worktrees with equal status counts have fingerprint %s", fingerprintA)
	}
}

func TestWorktreeFingerprintStableAndIncludesFilesystemState(t *testing.T) {
	repo := fingerprintTestRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "script.sh"), []byte("#!/bin/sh\necho first\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "ignored"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored", "state"), []byte("ignored first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("script.sh", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}

	initial := fingerprintTestValue(t, repo)
	if repeated := fingerprintTestValue(t, repo); repeated != initial {
		t.Fatalf("unchanged worktree fingerprint changed: %s != %s", initial, repeated)
	}

	if err := os.Chmod(filepath.Join(repo, "script.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed := fingerprintTestValue(t, repo); changed == initial {
		t.Fatal("executable-bit change did not change fingerprint")
	}
	if err := os.Chmod(filepath.Join(repo, "script.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored", "state"), []byte("ignored second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed := fingerprintTestValue(t, repo); changed == initial {
		t.Fatal("ignored-file content change did not change fingerprint")
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored", "state"), []byte("ignored first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "empty")); err != nil {
		t.Fatal(err)
	}
	if changed := fingerprintTestValue(t, repo); changed == initial {
		t.Fatal("empty-directory removal did not change fingerprint")
	}
	if err := os.Mkdir(filepath.Join(repo, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	if changed := fingerprintTestValue(t, repo); changed == initial {
		t.Fatal("symlink-target change did not change fingerprint")
	}
	if err := os.Remove(filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("script.sh", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "update-index", "--assume-unchanged", "tracked.txt")
	if changed := fingerprintTestValue(t, repo); changed == initial {
		t.Fatal("assume-unchanged index flag did not change fingerprint")
	}
	gitTestRun(t, repo, "update-index", "--no-assume-unchanged", "tracked.txt")
	gitTestRun(t, repo, "update-index", "--chmod=+x", "tracked.txt")
	if changed := fingerprintTestValue(t, repo); changed == initial {
		t.Fatal("logical index change did not change fingerprint")
	}
}

func fingerprintTestRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTestRun(t, repo, "add", ".gitignore", "tracked.txt")
	gitTestRun(t, repo, "commit", "-m", "initial")
	return repo
}

func fingerprintTestValue(t *testing.T, repo string) string {
	t.Helper()
	fingerprint, err := WorktreeFingerprintContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
