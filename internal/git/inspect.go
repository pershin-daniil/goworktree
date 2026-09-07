package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type RepositoryIdentity struct {
	SourcePath string
	CommonDir  string
}

type BaseSource string

const (
	BaseRequestOverride BaseSource = "request-override"
	BaseRepoPreference  BaseSource = "repository-preference"
	BaseRemoteHead      BaseSource = "remote-head"
	BaseCommonName      BaseSource = "common-name"
	BaseRepositoryHead  BaseSource = "repository-head"
)

type ResolvedBase struct {
	FullRef string
	OID     string
	Source  BaseSource
}

type WorktreeRegistration struct {
	Path     string
	HeadOID  string
	Branch   string
	Bare     bool
	Detached bool
	Locked   bool
	Prunable bool
}

type Checkout struct {
	Identity RepositoryIdentity
	FullRef  string
	HeadOID  string
	Detached bool
}

func ValidateBranchNameContext(ctx context.Context, repo, name string) error {
	if name == "" {
		return fmt.Errorf("branch name is empty")
	}
	out, err := runContext(ctx, repo, "check-ref-format", "--branch", name)
	if err != nil {
		return err
	}
	if strings.TrimSuffix(out, "\n") != name {
		return fmt.Errorf("git normalized branch name %q to %q", name, strings.TrimSpace(out))
	}
	return nil
}

func InspectRepositoryContext(ctx context.Context, path string) (RepositoryIdentity, error) {
	canonicalSource, err := canonicalExistingPath(path)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("canonical source path: %w", err)
	}
	out, err := runContext(ctx, canonicalSource,
		"rev-parse", "--is-inside-work-tree", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return RepositoryIdentity{}, err
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		return RepositoryIdentity{}, fmt.Errorf("git returned %d repository identity fields, expected 2", len(lines))
	}
	if lines[0] != "true" {
		return RepositoryIdentity{}, fmt.Errorf("%s is not a non-bare Git worktree", canonicalSource)
	}
	canonicalCommon, err := canonicalExistingPath(lines[1])
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("canonical Git common directory: %w", err)
	}
	return RepositoryIdentity{SourcePath: canonicalSource, CommonDir: canonicalCommon}, nil
}

func FetchRemoteContext(ctx context.Context, repo, remote string) error {
	if strings.TrimSpace(remote) == "" {
		return fmt.Errorf("remote is empty")
	}
	_, err := runContext(ctx, repo, "fetch", remote)
	return err
}

// ResolveNewWorkBaseContext resolves an immutable base for New Work. A request
// override is authoritative: when supplied but missing, it does not silently
// fall back. A missing configured preference may fall back to repository facts.
func ResolveNewWorkBaseContext(ctx context.Context, repo, remote, override, preference string) (ResolvedBase, error) {
	if override != "" {
		base, ok, err := resolveNamedBase(ctx, repo, remote, override, BaseRequestOverride)
		if err != nil {
			return ResolvedBase{}, err
		}
		if !ok {
			return ResolvedBase{}, fmt.Errorf("requested base %q not found", override)
		}
		return base, nil
	}

	if preference != "" {
		if base, ok, err := resolveNamedBase(ctx, repo, remote, preference, BaseRepoPreference); err != nil {
			return ResolvedBase{}, err
		} else if ok {
			return base, nil
		}
	}

	if remote != "" {
		remoteHead := "refs/remotes/" + remote + "/HEAD"
		if target, err := runContext(ctx, repo, "symbolic-ref", "--quiet", remoteHead); err == nil {
			if base, ok, resolveErr := resolveFullRef(ctx, repo, strings.TrimSpace(target), BaseRemoteHead); resolveErr != nil {
				return ResolvedBase{}, resolveErr
			} else if ok {
				return base, nil
			}
		}
	}

	for _, name := range []string{"main", "master", "develop"} {
		if base, ok, err := resolveNamedBase(ctx, repo, remote, name, BaseCommonName); err != nil {
			return ResolvedBase{}, err
		} else if ok {
			return base, nil
		}
	}

	headRef := "HEAD"
	if out, err := runContext(ctx, repo, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		headRef = strings.TrimSpace(out)
	}
	if base, ok, err := resolveFullRef(ctx, repo, headRef, BaseRepositoryHead); err != nil {
		return ResolvedBase{}, err
	} else if ok {
		return base, nil
	}
	return ResolvedBase{}, fmt.Errorf("could not resolve a base commit")
}

func LocalBranchOIDContext(ctx context.Context, repo, branch string) (string, bool, error) {
	fullRef := branch
	if !strings.HasPrefix(fullRef, "refs/heads/") {
		fullRef = "refs/heads/" + fullRef
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", fullRef+"^{commit}")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), true, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", false, fmt.Errorf("git rev-parse --verify --quiet %s: %w", fullRef, contextErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf("git rev-parse --verify --quiet %s: %w", fullRef, err)
}

func ListWorktreesContext(ctx context.Context, repo string) ([]WorktreeRegistration, error) {
	out, err := runContext(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var result []WorktreeRegistration
	var current WorktreeRegistration
	flush := func() {
		if current.Path != "" {
			current.Path = filepath.Clean(current.Path)
			result = append(result, current)
		}
		current = WorktreeRegistration{}
	}
	for _, field := range strings.Split(out, "\x00") {
		if field == "" {
			flush()
			continue
		}
		key, value, _ := strings.Cut(field, " ")
		switch key {
		case "worktree":
			current.Path = value
		case "HEAD":
			current.HeadOID = value
		case "branch":
			current.Branch = value
		case "bare":
			current.Bare = true
		case "detached":
			current.Detached = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	flush()
	return result, nil
}

// RootFileAtCommitContext checks a root path in the planned commit tree rather
// than the source checkout, which may be on a different branch or dirty.
func RootFileAtCommitContext(ctx context.Context, repo, oid, name string) (bool, error) {
	if strings.Contains(name, "/") || name == "" {
		return false, fmt.Errorf("root file name must be one component: %q", name)
	}
	out, err := runContext(ctx, repo, "ls-tree", "-z", "--name-only", oid, "--", name)
	if err != nil {
		return false, err
	}
	for _, entry := range strings.Split(out, "\x00") {
		if entry == name {
			return true, nil
		}
	}
	return false, nil
}

func CommitExistsContext(ctx context.Context, repo, oid string) error {
	if strings.TrimSpace(oid) == "" {
		return fmt.Errorf("commit OID is empty")
	}
	_, err := runContext(ctx, repo, "cat-file", "-e", oid+"^{commit}")
	return err
}

// DeleteLocalBranchAtOIDContext deletes one exact local ref only when it still
// points at expectedOID. Remote and remote-tracking refs are not addressed.
func DeleteLocalBranchAtOIDContext(ctx context.Context, repo, fullRef, expectedOID string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || strings.TrimPrefix(fullRef, "refs/heads/") == "" {
		return fmt.Errorf("not a full local branch ref: %q", fullRef)
	}
	if strings.TrimSpace(expectedOID) == "" {
		return fmt.Errorf("expected branch OID is empty")
	}
	observed, exists, err := LocalBranchOIDContext(ctx, repo, fullRef)
	if err != nil {
		return err
	}
	if !exists || observed != expectedOID {
		return fmt.Errorf("local branch %s changed: expected %s, observed %s", fullRef, expectedOID, observed)
	}
	_, err = runContext(ctx, repo, "update-ref", "-d", fullRef, expectedOID)
	return err
}

// UpdateLocalBranchAtOIDContext fast-forwards one non-checked-out local branch
// from an exact old OID to an exact descendant OID. It never addresses remote
// refs and never forces a non-fast-forward update.
func UpdateLocalBranchAtOIDContext(ctx context.Context, repo, fullRef, oldOID, newOID string) error {
	if _, err := localBranchName(fullRef); err != nil {
		return err
	}
	if strings.TrimSpace(oldOID) == "" || strings.TrimSpace(newOID) == "" {
		return fmt.Errorf("old and new branch OIDs are required")
	}
	observed, exists, err := LocalBranchOIDContext(ctx, repo, fullRef)
	if err != nil {
		return err
	}
	if !exists || observed != oldOID {
		return fmt.Errorf("local branch %s changed: expected %s, observed %s", fullRef, oldOID, observed)
	}
	worktrees, err := ListWorktreesContext(ctx, repo)
	if err != nil {
		return fmt.Errorf("list worktrees: %w", err)
	}
	for _, worktree := range worktrees {
		if worktree.Branch == fullRef {
			return fmt.Errorf("local branch %s is checked out at %s", fullRef, worktree.Path)
		}
	}
	ancestor, err := IsAncestorContext(ctx, repo, oldOID, newOID)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("refuse non-fast-forward update of %s", fullRef)
	}
	if _, err := runContext(ctx, repo, "update-ref", fullRef, newOID, oldOID); err != nil {
		return err
	}
	observed, exists, err = LocalBranchOIDContext(ctx, repo, fullRef)
	if err != nil {
		return fmt.Errorf("verify local branch %s: %w", fullRef, err)
	}
	if !exists || observed != newOID {
		return fmt.Errorf("verify local branch %s: expected %s, observed %s", fullRef, newOID, observed)
	}
	return nil
}

// FastForwardCheckoutAtOIDContext advances the exact checked-out local branch
// to a descendant commit without creating a merge commit or switching branches.
func FastForwardCheckoutAtOIDContext(ctx context.Context, repo, fullRef, oldOID, newOID string) error {
	if _, err := localBranchName(fullRef); err != nil {
		return err
	}
	checkout, err := InspectCheckoutContext(ctx, repo)
	if err != nil {
		return err
	}
	if checkout.FullRef != fullRef || checkout.HeadOID != oldOID {
		return fmt.Errorf("checkout changed: expected %s at %s, observed %s at %s", fullRef, oldOID, checkout.FullRef, checkout.HeadOID)
	}
	status, err := WorkingTreeStatusContext(ctx, repo)
	if err != nil {
		return err
	}
	if status.Dirty() {
		return fmt.Errorf("working tree has local changes")
	}
	operations, err := ActiveOperationsContext(ctx, repo)
	if err != nil {
		return err
	}
	if len(operations) > 0 {
		return fmt.Errorf("active Git operation: %s", operations[0])
	}
	ancestor, err := IsAncestorContext(ctx, repo, oldOID, newOID)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("refuse non-fast-forward update of %s", fullRef)
	}
	if _, err := runContext(ctx, repo, "merge", "--ff-only", "--no-edit", newOID); err != nil {
		return err
	}
	checkout, err = InspectCheckoutContext(ctx, repo)
	if err != nil {
		return fmt.Errorf("verify checkout: %w", err)
	}
	if checkout.FullRef != fullRef || checkout.HeadOID != newOID {
		return fmt.Errorf("verify checkout: expected %s at %s, observed %s at %s", fullRef, newOID, checkout.FullRef, checkout.HeadOID)
	}
	status, err = WorkingTreeStatusContext(ctx, repo)
	if err != nil {
		return fmt.Errorf("verify working tree: %w", err)
	}
	if status.Dirty() {
		return fmt.Errorf("working tree is not clean after fast-forward")
	}
	return nil
}

// RemoveWorktreeContext removes one exact registered worktree through its
// source repository, so it also works when the linked checkout is broken.
func RemoveWorktreeContext(ctx context.Context, source, destination string) error {
	return removeWorktreeContext(ctx, source, destination, true)
}

// RemoveCleanWorktreeContext removes a registered worktree without --force.
// Git therefore remains the final guard against a dirty-worktree race after a
// workflow's explicit clean-state and fingerprint checks.
func RemoveCleanWorktreeContext(ctx context.Context, source, destination string) error {
	return removeWorktreeContext(ctx, source, destination, false)
}

func removeWorktreeContext(ctx context.Context, source, destination string, force bool) error {
	canonicalSource, err := canonicalExistingPath(source)
	if err != nil {
		return fmt.Errorf("canonical source path: %w", err)
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("absolute worktree path: %w", err)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, filepath.Clean(destination))
	_, removeErr := runContext(ctx, canonicalSource, args...)
	_, pruneErr := runContext(ctx, canonicalSource, "worktree", "prune")
	return errors.Join(removeErr, pruneErr)
}

// PruneAndAttachWorktreeContext removes stale registrations, then attaches the
// exact existing local branch at a missing destination.
func PruneAndAttachWorktreeContext(ctx context.Context, source, destination, branchRef string) error {
	if _, err := runContext(ctx, source, "worktree", "prune"); err != nil {
		return err
	}
	return AttachWorktreeContext(ctx, source, destination, branchRef)
}

// RepairWorktreeRegistrationContext asks Git to rebuild administrative files
// for an existing linked checkout at one exact path.
func RepairWorktreeRegistrationContext(ctx context.Context, source, destination string) error {
	_, err := runContext(ctx, source, "worktree", "repair", destination)
	return err
}

// CreateWorktreeAtOIDContext creates exactly one new local branch and linked
// worktree from an immutable commit. It never resolves or updates a remote ref.
func CreateWorktreeAtOIDContext(ctx context.Context, repo, destination, branchRef, oid string) error {
	branch, err := localBranchName(branchRef)
	if err != nil {
		return err
	}
	_, err = runContext(ctx, repo, "worktree", "add", "-b", branch, destination, oid)
	return err
}

// AttachWorktreeContext attaches an already existing planned local branch.
// It is used only when reconciliation proves that branch was created at the
// exact planned OID by an interrupted New Work step.
func AttachWorktreeContext(ctx context.Context, repo, destination, branchRef string) error {
	branch, err := localBranchName(branchRef)
	if err != nil {
		return err
	}
	_, err = runContext(ctx, repo, "worktree", "add", destination, branch)
	return err
}

func InspectCheckoutContext(ctx context.Context, path string) (Checkout, error) {
	identity, err := InspectRepositoryContext(ctx, path)
	if err != nil {
		return Checkout{}, err
	}
	head, err := runContext(ctx, identity.SourcePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Checkout{}, err
	}
	checkout := Checkout{Identity: identity, HeadOID: strings.TrimSpace(head)}
	cmd := exec.CommandContext(ctx, "git", "symbolic-ref", "--quiet", "HEAD")
	cmd.Dir = identity.SourcePath
	out, err := cmd.Output()
	if err == nil {
		checkout.FullRef = strings.TrimSpace(string(out))
		return checkout, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return Checkout{}, fmt.Errorf("git symbolic-ref --quiet HEAD: %w", contextErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		checkout.Detached = true
		return checkout, nil
	}
	return Checkout{}, fmt.Errorf("git symbolic-ref --quiet HEAD: %w", err)
}

func localBranchName(fullRef string) (string, error) {
	branch, ok := strings.CutPrefix(fullRef, "refs/heads/")
	if !ok || branch == "" {
		return "", fmt.Errorf("not a full local branch ref: %q", fullRef)
	}
	return branch, nil
}

func resolveNamedBase(ctx context.Context, repo, remote, name string, source BaseSource) (ResolvedBase, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ResolvedBase{}, false, nil
	}
	if strings.HasPrefix(name, "refs/") || name == "HEAD" {
		return resolveFullRef(ctx, repo, name, source)
	}
	if remote != "" {
		if short, ok := strings.CutPrefix(name, remote+"/"); ok {
			return resolveFullRef(ctx, repo, "refs/remotes/"+remote+"/"+short, source)
		}
		if base, ok, err := resolveFullRef(ctx, repo, "refs/remotes/"+remote+"/"+name, source); err != nil || ok {
			return base, ok, err
		}
	}
	return resolveFullRef(ctx, repo, "refs/heads/"+name, source)
}

func resolveFullRef(ctx context.Context, repo, ref string, source BaseSource) (ResolvedBase, bool, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err == nil {
		return ResolvedBase{FullRef: ref, OID: strings.TrimSpace(string(out)), Source: source}, true, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return ResolvedBase{}, false, fmt.Errorf("git rev-parse --verify --quiet %s: %w", ref, contextErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return ResolvedBase{}, false, nil
	}
	return ResolvedBase{}, false, fmt.Errorf("git rev-parse --verify --quiet %s: %w", ref, err)
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
