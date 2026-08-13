package git

import (
	"bytes"
	"context"
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

type SyncStatus string

const (
	SyncRebased    SyncStatus = "rebased"
	SyncUpToDate   SyncStatus = "up-to-date"
	SyncConflict   SyncStatus = "conflict"
	SyncRolledBack SyncStatus = "rolled-back"
	SyncFailed     SyncStatus = "failed"
)

type SyncResult struct {
	Status SyncStatus
	From   string
	To     string
	Err    error
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

// PruneWorktrees removes stale worktree registrations from a primary repository.
// It is useful after a project folder was removed while one of its linked
// worktrees was already broken on disk.
func PruneWorktrees(repo string) error {
	gitDir, err := CommonDir(repo)
	if err != nil {
		return err
	}
	return prune(gitDir)
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

// SyncWorktree fetches origin and rebases the checked-out project branch onto
// origin/<base>. Local changes, including untracked files, are stashed and
// restored around the operation. A rebase conflict in a clean worktree is
// intentionally left in place so the caller can open the worktree for manual
// resolution and retry this operation. Conflicts after auto-stashing are still
// rolled back, because restoring that stash safely requires the original tree.
func SyncWorktree(repo, expectedBranch, base string) SyncResult {
	return SyncWorktreeContext(context.Background(), repo, expectedBranch, base)
}

// SyncWorktreeContext lets callers bound fetch/rebase time and cancel an
// interactive operation without leaving Git subprocesses running.
func SyncWorktreeContext(ctx context.Context, repo, expectedBranch, base string) SyncResult {
	if strings.TrimSpace(base) == "" {
		return SyncResult{Status: SyncFailed, Err: fmt.Errorf("base branch is empty")}
	}
	// During a rebase HEAD is detached, so this must run before normal branch
	// validation. The expected branch was validated when the rebase began.
	if rebaseInProgress(repo) {
		return ContinueRebaseContext(ctx, repo, expectedBranch, "")
	}
	if _, err := runContext(ctx, repo, "fetch", "origin"); err != nil {
		return SyncResult{Status: SyncFailed, Err: err}
	}
	target := remoteBase(base)
	to, err := revision(repo, target)
	if err != nil {
		return SyncResult{Status: SyncFailed, Err: fmt.Errorf("base %s: %w", target, err)}
	}
	return RebaseWorktreeToOIDContext(ctx, repo, expectedBranch, to)
}

// ContinueRebaseContext continues an already active rebase owned by a persisted
// workflow. Callers must establish that ownership before invoking it.
func ContinueRebaseContext(ctx context.Context, repo, expectedBranch, baseOID string) SyncResult {
	result := SyncResult{Status: SyncFailed}
	if !rebaseInProgress(repo) {
		result.Err = fmt.Errorf("no rebase is active")
		return result
	}
	if _, err := runContext(ctx, repo, "-c", "core.editor=true", "rebase", "--continue"); err != nil {
		if rebaseInProgress(repo) {
			result.Status = SyncConflict
			result.Err = fmt.Errorf("rebase conflict remains; resolve it, stage the files, then retry Sync Work: %w", err)
			return result
		}
		result.Err = fmt.Errorf("continue rebase: %w", err)
		return result
	}
	branch, err := CurrentBranch(repo)
	expectedBranch = strings.TrimPrefix(expectedBranch, "refs/heads/")
	if err != nil {
		result.Err = err
		return result
	}
	if expectedBranch != "" && branch != expectedBranch {
		result.Err = fmt.Errorf("continued rebase returned to branch %q, expected %q", branch, expectedBranch)
		return result
	}
	head, err := revision(repo, "HEAD")
	if err != nil {
		result.Err = err
		return result
	}
	if baseOID != "" {
		based, err := IsAncestorContext(ctx, repo, baseOID, head)
		if err != nil {
			result.Err = err
			return result
		}
		if !based {
			result.Err = fmt.Errorf("continued rebase result is not based on planned commit %s", shortRevision(baseOID))
			return result
		}
	}
	result.Status, result.To = SyncRebased, head
	return result
}

// RebaseWorktreeToOIDContext rebases onto one immutable commit without
// fetching. It preserves staged, unstaged, and untracked changes around the
// rebase and never addresses a user stash by a mutable selector.
func RebaseWorktreeToOIDContext(ctx context.Context, repo, expectedBranch, baseOID string) SyncResult {
	result := SyncResult{Status: SyncFailed, To: baseOID}
	if strings.TrimSpace(baseOID) == "" {
		result.Err = fmt.Errorf("base commit is empty")
		return result
	}
	if rebaseInProgress(repo) {
		result.Err = fmt.Errorf("rebase is already active")
		return result
	}
	branch, err := CurrentBranch(repo)
	if err != nil {
		result.Err = err
		return result
	}
	if branch == "HEAD" {
		result.Err = fmt.Errorf("detached HEAD")
		return result
	}
	expectedBranch = strings.TrimPrefix(expectedBranch, "refs/heads/")
	if expectedBranch != "" && branch != expectedBranch {
		result.Err = fmt.Errorf("current branch %q, expected %q", branch, expectedBranch)
		return result
	}
	from, err := revision(repo, "HEAD")
	if err != nil {
		result.Err = err
		return result
	}
	result.From = from

	alreadyBased, err := IsAncestorContext(ctx, repo, baseOID, from)
	if err != nil {
		result.Err = err
		return result
	}
	if alreadyBased {
		result.Status = SyncUpToDate
		return result
	}

	dirty, err := isDirty(repo)
	if err != nil {
		result.Err = err
		return result
	}
	stashed := false
	stashRef := ""
	if dirty {
		if _, err := runContext(ctx, repo, "stash", "push", "--include-untracked", "--message", "goworktree sync auto-stash"); err != nil {
			result.Err = err
			return result
		}
		stashRef, err = revision(repo, "refs/stash")
		if err != nil {
			result.Err = fmt.Errorf("identify auto-stash: %w", err)
			return result
		}
		stashed = true
	}

	restore := func() error {
		if !stashed {
			return nil
		}
		if _, err := runContext(ctx, repo, "stash", "apply", "--index", stashRef); err != nil {
			return err
		}
		return dropStash(repo, stashRef)
	}

	if _, err := runContext(ctx, repo,
		"-c", "rebase.autoStash=false",
		"-c", "rebase.updateRefs=false",
		"-c", "rerere.autoupdate=false",
		"rebase", "--no-autostash", "--no-rebase-merges", "--no-update-refs", "--no-rerere-autoupdate", "--verify", baseOID,
	); err != nil {
		if !stashed && rebaseInProgress(repo) {
			result.Status = SyncConflict
			result.Err = fmt.Errorf("rebase onto %s conflicted; resolve it and finish or abort the rebase", shortRevision(baseOID))
			return result
		}
		_, _ = run(repo, "rebase", "--abort")
		_, _ = run(repo, "reset", "--hard", from)
		if restoreErr := restore(); restoreErr != nil {
			result.Err = fmt.Errorf("rebase failed: %v; rollback auto-stash restore failed: %w (stash preserved)", err, restoreErr)
			return result
		}
		result.Status = SyncRolledBack
		result.Err = fmt.Errorf("rebase onto %s conflicted; rolled back to %s", shortRevision(baseOID), shortRevision(from))
		result.To = from
		return result
	}
	if err := restore(); err == nil {
		result.Status = SyncRebased
		if head, headErr := revision(repo, "HEAD"); headErr == nil {
			result.To = head
		}
		return result
	}

	// Applying the stash on the new commit conflicted. Remove only files created
	// by that failed apply, return to the original commit, and restore the stash
	// against the exact tree it came from.
	_, _ = run(repo, "reset", "--hard", from)
	_, _ = run(repo, "clean", "-fd")
	if err := restore(); err != nil {
		result.Err = fmt.Errorf("rollback to %s succeeded, but auto-stash restore failed: %w (stash preserved)", from, err)
		return result
	}
	result.Status = SyncRolledBack
	result.Err = fmt.Errorf("local changes conflict with %s; rolled back to %s", shortRevision(baseOID), from)
	result.To = from
	return result
}

// IsAncestorContext reports whether older is an ancestor of newer.
func IsAncestorContext(ctx context.Context, repo, older, newer string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", older, newer)
	cmd.Dir = repo
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return false, fmt.Errorf("git merge-base --is-ancestor: %w", contextErr)
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor: %w", err)
}

func rebaseInProgress(repo string) bool {
	_, err := run(repo, "rev-parse", "-q", "--verify", "REBASE_HEAD")
	return err == nil
}

func shortRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func remoteBase(base string) string {
	base = strings.TrimSpace(base)
	base = strings.TrimPrefix(base, "refs/heads/")
	base = strings.TrimPrefix(base, "refs/remotes/origin/")
	base = strings.TrimPrefix(base, "origin/")
	return "origin/" + base
}

func revision(repo, ref string) (string, error) {
	out, err := run(repo, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

// dropStash removes the exact stash object after it was applied. Git's
// `stash drop` only accepts reflog selectors, so locate its current selector
// immediately instead of assuming it remains stash@{0}.
func dropStash(repo, objectID string) error {
	out, err := run(repo, "stash", "list", "--format=%H")
	if err != nil {
		return err
	}
	for i, id := range strings.Fields(out) {
		if id == objectID {
			_, err := run(repo, "reflog", "delete", "--rewrite", "--updateref", fmt.Sprintf("refs/stash@{%d}", i))
			return err
		}
	}
	return fmt.Errorf("auto-stash %s no longer exists", shortRevision(objectID))
}

func isDirty(repo string) (bool, error) {
	out, err := run(repo, "status", "--porcelain", "--untracked-files=normal")
	return strings.TrimSpace(out) != "", err
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
	return runContext(context.Background(), dir, args...)
}

func runContext(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), contextErr)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), msg, err)
	}
	return string(out), nil
}
