package newwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
)

type Git interface {
	ValidateBranchName(context.Context, string, string) error
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	FetchRemote(context.Context, string, string) error
	ResolveBase(context.Context, string, string, string, string) (gitops.ResolvedBase, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	RootFileAtCommit(context.Context, string, string, string) (bool, error)
}

type RepositoryLocker interface {
	AcquireRepositories(context.Context, []string) (func() error, error)
}

type Planner struct {
	Git    Git
	Locker RepositoryLocker
	Now    func() time.Time
}

type SystemGit struct{}

func (SystemGit) ValidateBranchName(ctx context.Context, repo, name string) error {
	return gitops.ValidateBranchNameContext(ctx, repo, name)
}

func (SystemGit) InspectRepository(ctx context.Context, path string) (gitops.RepositoryIdentity, error) {
	return gitops.InspectRepositoryContext(ctx, path)
}

func (SystemGit) FetchRemote(ctx context.Context, repo, remote string) error {
	return gitops.FetchRemoteContext(ctx, repo, remote)
}

func (SystemGit) ResolveBase(ctx context.Context, repo, remote, override, preference string) (gitops.ResolvedBase, error) {
	return gitops.ResolveNewWorkBaseContext(ctx, repo, remote, override, preference)
}

func (SystemGit) LocalBranchOID(ctx context.Context, repo, branch string) (string, bool, error) {
	return gitops.LocalBranchOIDContext(ctx, repo, branch)
}

func (SystemGit) ListWorktrees(ctx context.Context, repo string) ([]gitops.WorktreeRegistration, error) {
	return gitops.ListWorktreesContext(ctx, repo)
}

func (SystemGit) RootFileAtCommit(ctx context.Context, repo, oid, name string) (bool, error) {
	return gitops.RootFileAtCommitContext(ctx, repo, oid, name)
}

type FileRepositoryLocker struct {
	Set lockops.Set
}

func (l FileRepositoryLocker) AcquireRepositories(ctx context.Context, identities []string) (func() error, error) {
	lease, err := l.Set.Acquire(ctx, "repositories", identities)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

type inspectedRepository struct {
	definition  Repository
	identity    gitops.RepositoryIdentity
	destination string
	fetchedAt   *time.Time
	base        gitops.ResolvedBase
	rootModule  bool
}

func (p Planner) Build(ctx context.Context, catalog Catalog, request Request) (Plan, error) {
	if p.Git == nil {
		return Plan{}, &ProblemsError{Problems: []Problem{{Code: CodeInternal, Operation: "initialize-planner", Cause: fmt.Errorf("Git adapter is nil")}}}
	}
	name, err := work.ParseName(request.Name)
	if err != nil {
		return Plan{}, problems(Problem{Code: CodeInvalidInput, Operation: "validate-work-name", Cause: err})
	}
	mode := request.Mode
	if mode == "" {
		mode = ModeOnline
	}
	if mode != ModeOnline && mode != ModeOffline {
		return Plan{}, problems(Problem{Code: CodeInvalidInput, Operation: "validate-mode", Cause: fmt.Errorf("unsupported mode %q", mode)})
	}
	if len(request.RepositoryIDs) == 0 {
		return Plan{}, problems(Problem{Code: CodeInvalidInput, Operation: "select-repositories", Cause: fmt.Errorf("no repositories selected")})
	}

	worksRoot, err := canonicalDirectory(catalog.WorksRoot)
	if err != nil {
		return Plan{}, problems(Problem{Code: CodeUnsafePath, Operation: "inspect-works-root", Path: catalog.WorksRoot, Cause: err})
	}
	workRoot, err := safeChild(worksRoot, name.String())
	if err != nil {
		return Plan{}, problems(Problem{Code: CodeUnsafePath, Operation: "resolve-work-root", Path: catalog.WorksRoot, Cause: err})
	}
	if collision, err := caseEquivalentEntry(worksRoot, name.String()); err != nil {
		return Plan{}, problems(Problem{Code: CodeInternal, Operation: "inspect-work-root", Path: workRoot, Cause: err})
	} else if collision != "" {
		return Plan{}, problems(Problem{Code: CodeAlreadyExists, Operation: "inspect-work-root", Path: filepath.Join(worksRoot, collision), Cause: fmt.Errorf("entry is case-equivalent to Work name %q", name)})
	}

	selected, selectProblems := selectRepositories(catalog, request, workRoot)
	if len(selectProblems) > 0 {
		return Plan{}, &ProblemsError{Problems: selectProblems}
	}

	inspected, inspectProblems := p.inspectSources(ctx, selected, name)
	if len(inspectProblems) > 0 {
		return Plan{}, &ProblemsError{Problems: inspectProblems}
	}
	if collisionProblems := p.inspectCollisions(ctx, inspected, name); len(collisionProblems) > 0 {
		return Plan{}, &ProblemsError{Problems: collisionProblems}
	}

	if mode == ModeOnline {
		if p.Locker == nil {
			return Plan{}, problems(Problem{Code: CodeInternal, Operation: "prepare-remotes", Cause: fmt.Errorf("repository locker is nil")})
		}
		identities := make([]string, 0, len(inspected))
		for _, repo := range inspected {
			identities = append(identities, repo.identity.CommonDir)
		}
		release, err := p.Locker.AcquireRepositories(ctx, identities)
		if err != nil {
			code := codeFor(err, CodeInternal)
			if errors.Is(err, lockops.ErrLocked) {
				code = CodeLocked
			}
			return Plan{}, problems(Problem{Code: code, Operation: "lock-repositories", Cause: err})
		}

		var fetchProblems []Problem
		identityProblems := p.reinspectSourceIdentities(ctx, inspected)
		fetchProblems = append(fetchProblems, identityProblems...)
		if len(identityProblems) == 0 {
			for i := range inspected {
				repo := &inspected[i]
				if repo.definition.Remote == "" {
					fetchProblems = append(fetchProblems, Problem{Code: CodeInvalidInput, Operation: "fetch-remote", RepositoryID: repo.definition.ID, Cause: fmt.Errorf("configured remote is empty")})
					continue
				}
				if err := p.Git.FetchRemote(ctx, repo.identity.SourcePath, repo.definition.Remote); err != nil {
					fetchProblems = append(fetchProblems, Problem{Code: codeFor(err, CodeExternalFailure), Operation: "fetch-remote", RepositoryID: repo.definition.ID, Cause: err})
					continue
				}
				now := p.now().UTC()
				repo.fetchedAt = &now
			}
		}
		if len(fetchProblems) == 0 {
			inspectProblems = p.inspectMutableGitFacts(ctx, inspected, request, name)
		}
		if len(fetchProblems) == 0 && len(inspectProblems) == 0 {
			if collision, collisionErr := caseEquivalentEntry(worksRoot, name.String()); collisionErr != nil {
				inspectProblems = append(inspectProblems, Problem{Code: CodeInternal, Operation: "reinspect-work-root", Path: workRoot, Cause: collisionErr})
			} else if collision != "" {
				inspectProblems = append(inspectProblems, Problem{Code: CodeStateConflict, Operation: "reinspect-work-root", Path: filepath.Join(worksRoot, collision), Cause: fmt.Errorf("Work root appeared during planning")})
			}
		}
		if err := release(); err != nil {
			fetchProblems = append(fetchProblems, Problem{Code: CodeInternal, Operation: "release-repository-locks", Cause: err})
		}
		fetchProblems = append(fetchProblems, inspectProblems...)
		if len(fetchProblems) > 0 {
			return Plan{}, &ProblemsError{Problems: fetchProblems}
		}
	} else {
		if inspectProblems := p.inspectMutableGitFacts(ctx, inspected, request, name); len(inspectProblems) > 0 {
			return Plan{}, &ProblemsError{Problems: inspectProblems}
		}
	}

	plan := Plan{
		WorkName:         name,
		WorkRoot:         workRoot,
		ManifestPath:     filepath.Join(workRoot, ".goworktree.json"),
		Mode:             mode,
		OpenProgram:      request.OpenProgram,
		NoRemoteMutation: true,
		Repositories:     make([]RepositoryPlan, 0, len(inspected)),
	}
	for _, repo := range inspected {
		plan.Repositories = append(plan.Repositories, RepositoryPlan{
			ID:              repo.definition.ID,
			SourcePath:      repo.identity.SourcePath,
			GitCommonDir:    repo.identity.CommonDir,
			Remote:          repo.definition.Remote,
			FetchedAt:       repo.fetchedAt,
			BaseRef:         repo.base.FullRef,
			BaseOID:         repo.base.OID,
			TargetBranchRef: "refs/heads/" + name.String(),
			Destination:     repo.destination,
			IncludeInGoWork: repo.rootModule,
		})
	}
	return plan, nil
}

func (p Planner) inspectSources(ctx context.Context, selected []inspectedRepository, name work.Name) ([]inspectedRepository, []Problem) {
	result := append([]inspectedRepository(nil), selected...)
	seenIdentity := make(map[string]string, len(result))
	var found []Problem
	for i := range result {
		repo := &result[i]
		identity, err := p.Git.InspectRepository(ctx, repo.definition.SourcePath)
		if err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeNotFound), Operation: "inspect-source", RepositoryID: repo.definition.ID, Path: repo.definition.SourcePath, Cause: err})
			continue
		}
		repo.identity = identity
		if first, exists := seenIdentity[identity.CommonDir]; exists {
			found = append(found, Problem{Code: CodeInvalidInput, Operation: "deduplicate-repository-identity", RepositoryID: repo.definition.ID, Path: identity.CommonDir, Cause: fmt.Errorf("same Git repository already selected as %q", first)})
		} else {
			seenIdentity[identity.CommonDir] = repo.definition.ID
		}
		if err := p.Git.ValidateBranchName(ctx, identity.SourcePath, name.String()); err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeInvalidInput), Operation: "validate-branch-name", RepositoryID: repo.definition.ID, Cause: err})
		}
	}
	return result, found
}

func (p Planner) reinspectSourceIdentities(ctx context.Context, repositories []inspectedRepository) []Problem {
	var found []Problem
	for _, repo := range repositories {
		identity, err := p.Git.InspectRepository(ctx, repo.definition.SourcePath)
		if err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeStateConflict), Operation: "reinspect-source", RepositoryID: repo.definition.ID, Path: repo.definition.SourcePath, Cause: err})
			continue
		}
		if identity != repo.identity {
			found = append(found, Problem{Code: CodeStateConflict, Operation: "reinspect-source", RepositoryID: repo.definition.ID, Path: repo.definition.SourcePath, Cause: fmt.Errorf("repository identity changed from %q to %q", repo.identity.CommonDir, identity.CommonDir)})
		}
	}
	return found
}

func (p Planner) inspectMutableGitFacts(ctx context.Context, repositories []inspectedRepository, request Request, name work.Name) []Problem {
	found := p.inspectCollisions(ctx, repositories, name)
	for i := range repositories {
		repo := &repositories[i]
		override := request.BaseOverrides[repo.definition.ID]
		base, err := p.Git.ResolveBase(ctx, repo.identity.SourcePath, repo.definition.Remote, override, repo.definition.BasePreference)
		if err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeNotFound), Operation: "resolve-base", RepositoryID: repo.definition.ID, Cause: err})
			continue
		}
		rootModule, err := p.Git.RootFileAtCommit(ctx, repo.identity.SourcePath, base.OID, "go.mod")
		if err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeInternal), Operation: "inspect-root-module", RepositoryID: repo.definition.ID, Cause: err})
			continue
		}
		repo.base = base
		repo.rootModule = rootModule
	}
	return found
}

func (p Planner) inspectCollisions(ctx context.Context, repositories []inspectedRepository, name work.Name) []Problem {
	var found []Problem
	targetRef := "refs/heads/" + name.String()
	for _, repo := range repositories {
		if oid, exists, err := p.Git.LocalBranchOID(ctx, repo.identity.SourcePath, targetRef); err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeInternal), Operation: "inspect-target-branch", RepositoryID: repo.definition.ID, Cause: err})
		} else if exists {
			found = append(found, Problem{Code: CodeAlreadyExists, Operation: "inspect-target-branch", RepositoryID: repo.definition.ID, Cause: fmt.Errorf("%s exists at %s", targetRef, oid)})
		}

		worktrees, err := p.Git.ListWorktrees(ctx, repo.identity.SourcePath)
		if err != nil {
			found = append(found, Problem{Code: codeFor(err, CodeInternal), Operation: "inspect-worktree-registrations", RepositoryID: repo.definition.ID, Cause: err})
		} else {
			for _, registration := range worktrees {
				if registration.Branch == targetRef {
					found = append(found, Problem{Code: CodeAlreadyExists, Operation: "inspect-checked-out-branch", RepositoryID: repo.definition.ID, Path: registration.Path, Cause: fmt.Errorf("%s is already checked out", targetRef)})
				}
				if sameCleanPath(registration.Path, repo.destination) {
					found = append(found, Problem{Code: CodeStateConflict, Operation: "inspect-destination-registration", RepositoryID: repo.definition.ID, Path: repo.destination, Cause: fmt.Errorf("Git already has a worktree registration for the destination")})
				}
			}
		}
	}
	return found
}

func selectRepositories(catalog Catalog, request Request, workRoot string) ([]inspectedRepository, []Problem) {
	seenIDs := make(map[string]struct{}, len(request.RepositoryIDs))
	selectedSet := make(map[string]struct{}, len(request.RepositoryIDs))
	var found []Problem
	for _, id := range request.RepositoryIDs {
		if _, exists := seenIDs[id]; exists {
			found = append(found, Problem{Code: CodeInvalidInput, Operation: "select-repositories", RepositoryID: id, Cause: fmt.Errorf("repository ID is duplicated")})
		}
		seenIDs[id] = struct{}{}
		selectedSet[id] = struct{}{}
	}
	for id := range request.BaseOverrides {
		if _, selected := selectedSet[id]; !selected {
			found = append(found, Problem{Code: CodeInvalidInput, Operation: "validate-base-override", RepositoryID: id, Cause: fmt.Errorf("base override targets an unselected repository")})
		}
	}
	if len(found) > 0 {
		return nil, found
	}

	ids := append([]string(nil), request.RepositoryIDs...)
	sort.Strings(ids)
	result := make([]inspectedRepository, 0, len(ids))
	seenFolders := make(map[string]string, len(ids))
	for _, id := range ids {
		repo, exists := catalog.Repositories[id]
		if !exists {
			found = append(found, Problem{Code: CodeNotFound, Operation: "select-repository", RepositoryID: id, Cause: fmt.Errorf("repository is not configured")})
			continue
		}
		if repo.ID != "" && repo.ID != id {
			found = append(found, Problem{Code: CodeInvalidInput, Operation: "validate-repository-id", RepositoryID: id, Cause: fmt.Errorf("catalog entry declares ID %q", repo.ID)})
			continue
		}
		repo.ID = id
		destination, err := safeChild(workRoot, repo.Folder)
		if err != nil {
			found = append(found, Problem{Code: CodeUnsafePath, Operation: "resolve-destination", RepositoryID: id, Path: workRoot, Cause: err})
			continue
		}
		folderKey, first, exists := equivalentFolder(seenFolders, repo.Folder)
		if exists {
			found = append(found, Problem{Code: CodeInvalidInput, Operation: "deduplicate-destination", RepositoryID: id, Path: destination, Cause: fmt.Errorf("folder is case-equivalent to repository %q", first)})
			continue
		}
		seenFolders[folderKey] = id
		result = append(result, inspectedRepository{definition: repo, destination: destination})
	}
	return result, found
}

func equivalentFolder(seen map[string]string, folder string) (string, string, bool) {
	for existing, repositoryID := range seen {
		if strings.EqualFold(existing, folder) {
			return existing, repositoryID, true
		}
	}
	return folder, "", false
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return canonical, nil
}

func safeChild(root, child string) (string, error) {
	if child == "" || child == "." || child == ".." || filepath.IsAbs(child) || strings.ContainsAny(child, `/\\`) {
		return "", fmt.Errorf("%q is not one path component", child)
	}
	path := filepath.Join(root, child)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %q", child)
	}
	return path, nil
}

func caseEquivalentEntry(root, name string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return entry.Name(), nil
		}
	}
	return "", nil
}

func sameCleanPath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}

func (p Planner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func problems(items ...Problem) error {
	return &ProblemsError{Problems: items}
}

func codeFor(err error, fallback ErrorCode) ErrorCode {
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeTimeout
	}
	if errors.Is(err, context.Canceled) {
		return CodeInterrupted
	}
	return fallback
}
