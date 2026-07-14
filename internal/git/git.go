package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ScannedRepo struct {
	ID    string
	Path  string
	Alias string
}

func IsRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// IsMainRepo reports whether path is a primary clone (.git directory), not a linked worktree.
func IsMainRepo(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && info.IsDir()
}

func CurrentBranch(repo string) (string, error) {
	out, err := run(repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func DefaultBranch(repo string) (string, error) {
	out, err := run(repo, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		if name, ok := strings.CutPrefix(ref, "origin/"); ok && name != "" && name != "HEAD" {
			return name, nil
		}
	}

	out, err = run(repo, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(out)
		if parts := strings.Split(ref, "/"); len(parts) > 0 {
			name := parts[len(parts)-1]
			if name != "" && name != "HEAD" {
				return name, nil
			}
		}
	}

	for _, name := range []string{"main", "master", "develop"} {
		if BranchExists(repo, name) || RemoteBranchExists(repo, "origin", name) {
			return name, nil
		}
	}

	return CurrentBranch(repo)
}

func BranchExists(repo, branch string) bool {
	_, err := run(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// RemoteBranchExists reports whether refs/remotes/<remote>/<branch> exists.
func RemoteBranchExists(repo, remote, branch string) bool {
	if remote == "" || branch == "" {
		return false
	}
	_, err := run(repo, "show-ref", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
	return err == nil
}

// RefExists reports whether ref resolves to a commit (branch, remote, tag, etc.).
func RefExists(repo, ref string) bool {
	if ref == "" {
		return false
	}
	_, err := run(repo, "rev-parse", "--verify", ref+"^{commit}")
	return err == nil
}

// ResolveBaseBranch picks a start-point for a new project branch.
// Prefers preferred when it exists locally or as origin/<preferred>;
// otherwise detects the repo default (origin/HEAD, then main/master/develop, then HEAD).
func ResolveBaseBranch(repo, preferred string) (string, error) {
	if base, ok := resolveRef(repo, preferred); ok {
		return base, nil
	}

	detected, err := DefaultBranch(repo)
	if err == nil {
		if base, ok := resolveRef(repo, detected); ok {
			return base, nil
		}
	}

	if preferred != "" {
		return "", fmt.Errorf("base branch %q not found (could not detect a default in %s)", preferred, repo)
	}
	if err != nil {
		return "", fmt.Errorf("could not detect default branch in %s: %w", repo, err)
	}
	return "", fmt.Errorf("could not detect default branch in %s", repo)
}

func resolveRef(repo, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if BranchExists(repo, name) {
		return name, true
	}
	if RemoteBranchExists(repo, "origin", name) {
		return "origin/" + name, true
	}
	return "", false
}

// SameBranchRef reports whether preferred and resolved name the same branch
// (e.g. main vs origin/main).
func SameBranchRef(preferred, resolved string) bool {
	return branchShortName(preferred) == branchShortName(resolved)
}

func branchShortName(ref string) string {
	ref = strings.TrimSpace(ref)
	ref = strings.TrimPrefix(ref, "refs/heads/")
	ref = strings.TrimPrefix(ref, "refs/remotes/origin/")
	ref = strings.TrimPrefix(ref, "origin/")
	return ref
}

// AddProjectWorktree creates a worktree on newBranch from baseBranch when needed.
func AddProjectWorktree(repo, dest, newBranch, baseBranch string) error {
	if !IsRepo(repo) {
		return fmt.Errorf("%s is not a git repository", repo)
	}
	if baseBranch == "" {
		return fmt.Errorf("base branch is empty")
	}
	if !RefExists(repo, baseBranch) {
		return fmt.Errorf("base branch %q not found in %s", baseBranch, repo)
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination already exists: %s", dest)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if BranchExists(repo, newBranch) {
		_, err := run(repo, "worktree", "add", dest, newBranch)
		return err
	}
	_, err := run(repo, "worktree", "add", "-b", newBranch, dest, baseBranch)
	return err
}

func RemoveWorktree(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	gitDir, commonErr := CommonDir(abs)
	if _, err := run(abs, "worktree", "remove", "--force", abs); err == nil {
		if commonErr == nil && gitDir != "" {
			_ = prune(gitDir)
		}
		return nil
	}
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	if commonErr == nil && gitDir != "" {
		_ = prune(gitDir)
	}
	return nil
}

func prune(gitDir string) error {
	return exec.Command("git", "--git-dir="+gitDir, "worktree", "prune").Run()
}

func CommonDir(path string) (string, error) {
	out, err := run(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(out)
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	return filepath.Clean(common), nil
}

// MainRepo returns the primary clone path for a worktree (parent of .git common dir).
func MainRepo(path string) (string, error) {
	common, err := CommonDir(path)
	if err != nil {
		return "", err
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	// bare common dir — treat as the repo itself
	return common, nil
}

func DeleteBranch(gitDir, branch string) error {
	if branch == "" || branch == "HEAD" {
		return nil
	}
	cmd := exec.Command("git", "--git-dir="+gitDir, "branch", "-D", branch)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "not found") {
			return nil
		}
		return fmt.Errorf("git branch -D %s: %s", branch, msg)
	}
	return nil
}

// ScanRepos walks root up to maxDepth levels and returns primary clones only.
func ScanRepos(root string, maxDepth int) ([]ScannedRepo, error) {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	root = filepath.Clean(root)

	var repos []ScannedRepo
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}

		name := d.Name()
		if name == "node_modules" || name == "vendor" ||
			(strings.HasPrefix(name, ".") && name != ".") {
			return filepath.SkipDir
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(filepath.ToSlash(rel), "/") + 1
		}
		if depth > maxDepth {
			return filepath.SkipDir
		}
		if depth == 0 {
			return nil
		}

		if IsMainRepo(path) {
			repos = append(repos, ScannedRepo{
				Path:  path,
				Alias: filepath.Base(path),
			})
			return filepath.SkipDir
		}
		return nil
	})
	return repos, err
}

func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}
