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
	inside, err := runContext(ctx, canonicalSource, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return RepositoryIdentity{}, err
	}
	if strings.TrimSpace(inside) != "true" {
		return RepositoryIdentity{}, fmt.Errorf("%s is not a non-bare Git worktree", canonicalSource)
	}
	common, err := commonDirContext(ctx, canonicalSource)
	if err != nil {
		return RepositoryIdentity{}, err
	}
	canonicalCommon, err := canonicalExistingPath(common)
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
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", fullRef)
	cmd.Dir = repo
	err := cmd.Run()
	if err == nil {
		oid, resolveErr := runContext(ctx, repo, "rev-parse", "--verify", fullRef+"^{commit}")
		if resolveErr != nil {
			return "", false, resolveErr
		}
		return strings.TrimSpace(oid), true, nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", false, fmt.Errorf("git show-ref --verify --quiet %s: %w", fullRef, contextErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", false, nil
	}
	return "", false, fmt.Errorf("git show-ref --verify --quiet %s: %w", fullRef, err)
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

func commonDirContext(ctx context.Context, path string) (string, error) {
	out, err := runContext(ctx, path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(out)
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	return filepath.Clean(common), nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
