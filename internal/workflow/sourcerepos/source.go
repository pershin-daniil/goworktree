package sourcerepos

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
)

type Git interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	RefOID(context.Context, string, string) (string, bool, error)
	IsAncestor(context.Context, string, string, string) (bool, error)
	FetchRemote(context.Context, string, string) error
	CommitExists(context.Context, string, string) error
	UpdateLocalBranch(context.Context, string, string, string, string) error
	FastForwardCheckout(context.Context, string, string, string, string) error
}

type SystemGit struct{}

func (SystemGit) InspectRepository(ctx context.Context, path string) (gitops.RepositoryIdentity, error) {
	return gitops.InspectRepositoryContext(ctx, path)
}
func (SystemGit) InspectCheckout(ctx context.Context, path string) (gitops.Checkout, error) {
	return gitops.InspectCheckoutContext(ctx, path)
}
func (SystemGit) WorkingTreeStatus(ctx context.Context, path string) (gitops.WorkingTreeStatus, error) {
	return gitops.WorkingTreeStatusContext(ctx, path)
}
func (SystemGit) ActiveOperations(ctx context.Context, path string) ([]gitops.ActiveOperation, error) {
	return gitops.ActiveOperationsContext(ctx, path)
}
func (SystemGit) ListWorktrees(ctx context.Context, path string) ([]gitops.WorktreeRegistration, error) {
	return gitops.ListWorktreesContext(ctx, path)
}
func (SystemGit) RefOID(ctx context.Context, path, ref string) (string, bool, error) {
	return gitops.RefOIDContext(ctx, path, ref)
}
func (SystemGit) IsAncestor(ctx context.Context, path, older, newer string) (bool, error) {
	return gitops.IsAncestorContext(ctx, path, older, newer)
}
func (SystemGit) FetchRemote(ctx context.Context, path, remote string) error {
	return gitops.FetchRemoteContext(ctx, path, remote)
}
func (SystemGit) CommitExists(ctx context.Context, path, oid string) error {
	return gitops.CommitExistsContext(ctx, path, oid)
}
func (SystemGit) UpdateLocalBranch(ctx context.Context, path, ref, oldOID, newOID string) error {
	return gitops.UpdateLocalBranchAtOIDContext(ctx, path, ref, oldOID, newOID)
}
func (SystemGit) FastForwardCheckout(ctx context.Context, path, ref, oldOID, newOID string) error {
	return gitops.FastForwardCheckoutAtOIDContext(ctx, path, ref, oldOID, newOID)
}

type Locker interface {
	Acquire(context.Context, []string) (func() error, error)
}

type FileLocker struct{ Set lockops.Set }

func (l FileLocker) Acquire(ctx context.Context, repositories []string) (func() error, error) {
	lease, err := l.Set.Acquire(ctx, "repositories", unique(repositories))
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

type Inspector struct {
	Git Git
	Now func() time.Time
}

func (i Inspector) Inspect(ctx context.Context, configs []RepositoryConfig) Snapshot {
	snapshot := Snapshot{InspectedAt: i.now().UTC()}
	for _, config := range sortedConfigs(configs) {
		if err := ctx.Err(); err != nil {
			snapshot.Repositories = append(snapshot.Repositories, RepositorySnapshot{
				RepositoryConfig: config, InspectedAt: snapshot.InspectedAt, Relation: RelationUnknown, Problem: err.Error(),
			})
			continue
		}
		snapshot.Repositories = append(snapshot.Repositories, i.inspectOne(ctx, config, time.Time{}))
	}
	return snapshot
}

func (i Inspector) inspectOne(ctx context.Context, config RepositoryConfig, fetchedAt time.Time) RepositorySnapshot {
	observed := RepositorySnapshot{
		RepositoryConfig: config, InspectedAt: i.now().UTC(), FetchedAt: fetchedAt, Relation: RelationUnknown,
	}
	localRef, remoteRef, err := branchRefs(config.Remote, config.DefaultBranch)
	if err != nil {
		observed.Problem = err.Error()
		return observed
	}
	observed.LocalRef, observed.RemoteRef = localRef, remoteRef
	if strings.TrimSpace(config.ID) == "" || strings.TrimSpace(config.Path) == "" {
		observed.Problem = "repository ID and path are required"
		return observed
	}
	identity, err := i.Git.InspectRepository(ctx, config.Path)
	if err != nil {
		observed.Problem = "inspect repository identity: " + err.Error()
		return observed
	}
	observed.Identity, observed.IdentityKnown = identity, true
	if checkout, err := i.Git.InspectCheckout(ctx, identity.SourcePath); err != nil {
		observed.Problem = appendProblem(observed.Problem, "inspect source checkout: "+err.Error())
	} else {
		observed.Checkout, observed.CheckoutKnown = checkout, true
	}
	if status, err := i.Git.WorkingTreeStatus(ctx, identity.SourcePath); err != nil {
		observed.Problem = appendProblem(observed.Problem, "inspect working tree: "+err.Error())
	} else {
		observed.WorkingTree, observed.WorkingKnown = status, true
	}
	if operations, err := i.Git.ActiveOperations(ctx, identity.SourcePath); err != nil {
		observed.Problem = appendProblem(observed.Problem, "inspect Git operations: "+err.Error())
	} else {
		observed.Operations, observed.OperationsKnown = operations, true
	}
	if worktrees, err := i.Git.ListWorktrees(ctx, identity.SourcePath); err != nil {
		observed.Problem = appendProblem(observed.Problem, "inspect worktrees: "+err.Error())
	} else {
		observed.Worktrees, observed.WorktreesKnown = worktrees, true
		for _, worktree := range worktrees {
			if filepath.Clean(worktree.Path) == filepath.Clean(identity.SourcePath) {
				continue
			}
			operations, operationErr := i.Git.ActiveOperations(ctx, worktree.Path)
			if operationErr != nil {
				observed.OperationsKnown = false
				observed.Problem = appendProblem(observed.Problem, "inspect Git operations at "+worktree.Path+": "+operationErr.Error())
				continue
			}
			observed.Operations = append(observed.Operations, operations...)
		}
	}
	if oid, exists, err := i.Git.RefOID(ctx, identity.SourcePath, localRef); err != nil {
		observed.Problem = appendProblem(observed.Problem, "inspect local default: "+err.Error())
	} else if exists {
		observed.LocalOID, observed.LocalKnown = oid, true
	}
	if oid, exists, err := i.Git.RefOID(ctx, identity.SourcePath, remoteRef); err != nil {
		observed.Problem = appendProblem(observed.Problem, "inspect remote default: "+err.Error())
	} else if exists {
		observed.RemoteOID, observed.RemoteKnown = oid, true
	}
	observed.Relation, err = relation(ctx, i.Git, identity.SourcePath, observed.LocalOID, observed.LocalKnown, observed.RemoteOID, observed.RemoteKnown)
	if err != nil {
		observed.Problem = appendProblem(observed.Problem, "compare default branches: "+err.Error())
		observed.Relation = RelationUnknown
	}
	return observed
}

func (i Inspector) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}

type Fetcher struct {
	Git         Git
	Locker      Locker
	Now         func() time.Time
	Parallelism int
}

func (f Fetcher) Fetch(ctx context.Context, configs []RepositoryConfig) (result Result, returnErr error) {
	if f.Git == nil || f.Locker == nil {
		return result, fmt.Errorf("Source Repository Fetch dependencies are incomplete")
	}
	configs = sortedConfigs(configs)
	identities, problems := inspectIdentities(ctx, f.Git, configs)
	keys := identityKeys(identities)
	var release func() error
	if len(keys) > 0 {
		var err error
		release, err = f.Locker.Acquire(ctx, keys)
		if err != nil {
			return result, fmt.Errorf("acquire source repository locks: %w", err)
		}
		defer func() { returnErr = errors.Join(returnErr, release()) }()
	}
	result.Repositories = make([]RepositoryResult, len(configs))
	inspector := Inspector{Git: f.Git, Now: f.Now}
	parallelRepositories(len(configs), f.Parallelism, func(index int) {
		config := configs[index]
		result.Repositories[index] = f.fetchOne(ctx, inspector, config, identities[config.ID], problems[config.ID])
	})
	return result, nil
}

func (f Fetcher) fetchOne(ctx context.Context, inspector Inspector, config RepositoryConfig, identity gitops.RepositoryIdentity, problem string) RepositoryResult {
	failed := RepositoryResult{ID: config.ID, Path: config.Path, Status: StatusFailed}
	if problem != "" {
		failed.Err = errors.New(problem)
		return failed
	}
	if err := ctx.Err(); err != nil {
		failed.Err = err
		return failed
	}
	if err := revalidateIdentity(ctx, f.Git, config.Path, identity); err != nil {
		failed.Err = fmt.Errorf("revalidate repository identity: %w", err)
		return failed
	}
	if err := f.Git.FetchRemote(ctx, identity.SourcePath, config.Remote); err != nil {
		failed.Err = fmt.Errorf("fetch %s: %w", config.Remote, err)
		return failed
	}
	observed := inspector.inspectOne(ctx, config, f.now().UTC())
	entry := RepositoryResult{ID: config.ID, Path: config.Path, Status: StatusFetched, Relation: observed.Relation, Snapshot: observed}
	if observed.Problem != "" {
		entry.Status, entry.Err = StatusFailed, errors.New(observed.Problem)
	} else if !observed.RemoteKnown {
		entry.Status, entry.Err = StatusFailed, errors.New("remote default branch is missing after fetch")
	}
	return entry
}

func (f Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

type Planner struct {
	Git         Git
	Locker      Locker
	Now         func() time.Time
	Parallelism int
}

func (p Planner) Build(ctx context.Context, configs []RepositoryConfig) (plan Plan, returnErr error) {
	if p.Git == nil || p.Locker == nil {
		return plan, fmt.Errorf("Source Repository Update planner dependencies are incomplete")
	}
	configs = sortedConfigs(configs)
	plan.BuiltAt = p.now().UTC()
	identities, problems := inspectIdentities(ctx, p.Git, configs)
	keys := identityKeys(identities)
	var release func() error
	if len(keys) > 0 {
		var err error
		release, err = p.Locker.Acquire(ctx, keys)
		if err != nil {
			return plan, fmt.Errorf("acquire source repository planning locks: %w", err)
		}
		defer func() { returnErr = errors.Join(returnErr, release()) }()
	}
	plan.Repositories = make([]RepositoryPlan, len(configs))
	inspector := Inspector{Git: p.Git, Now: p.Now}
	parallelRepositories(len(configs), p.Parallelism, func(index int) {
		config := configs[index]
		plan.Repositories[index] = p.planOne(ctx, inspector, config, identities[config.ID], problems[config.ID])
	})
	return plan, nil
}

func (p Planner) planOne(ctx context.Context, inspector Inspector, config RepositoryConfig, identity gitops.RepositoryIdentity, problem string) RepositoryPlan {
	entry := RepositoryPlan{RepositoryConfig: config, Action: ActionFailed}
	if problem != "" {
		entry.Reason = problem
		return entry
	}
	entry.GitCommonDir = identity.CommonDir
	if err := ctx.Err(); err != nil {
		entry.Reason = err.Error()
		return entry
	}
	if err := revalidateIdentity(ctx, p.Git, config.Path, identity); err != nil {
		entry.Reason = "revalidate repository identity: " + err.Error()
		return entry
	}
	if err := p.Git.FetchRemote(ctx, identity.SourcePath, config.Remote); err != nil {
		entry.Reason = "fetch " + config.Remote + ": " + err.Error()
		return entry
	}
	return planFromSnapshot(inspector.inspectOne(ctx, config, p.now().UTC()))
}

func (p Planner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

const defaultRepositoryParallelism = 4

func parallelRepositories(total, requested int, run func(int)) {
	if total <= 0 {
		return
	}
	workers := requested
	if workers <= 0 {
		workers = defaultRepositoryParallelism
	}
	if workers > total {
		workers = total
	}
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for index := range jobs {
				run(index)
			}
		}()
	}
	for index := range total {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
}

func planFromSnapshot(observed RepositorySnapshot) RepositoryPlan {
	entry := RepositoryPlan{
		RepositoryConfig: observed.RepositoryConfig, FetchedAt: observed.FetchedAt,
		LocalRef: observed.LocalRef, LocalOID: observed.LocalOID, RemoteRef: observed.RemoteRef,
		TargetOID: observed.RemoteOID, Relation: observed.Relation, Action: ActionFailed,
	}
	if observed.IdentityKnown {
		entry.GitCommonDir = observed.Identity.CommonDir
	}
	if observed.Problem != "" {
		entry.Reason = observed.Problem
		return entry
	}
	if !observed.LocalKnown {
		entry.Action, entry.Reason = ActionSkip, "local default branch is missing"
		return entry
	}
	if !observed.RemoteKnown {
		entry.Action, entry.Reason = ActionFailed, "remote default branch is missing"
		return entry
	}
	switch observed.Relation {
	case RelationEqual:
		entry.Action = ActionUnchanged
		return entry
	case RelationAhead:
		entry.Action, entry.Reason = ActionUnchanged, "local default branch is ahead; remote is not modified"
		return entry
	case RelationDiverged:
		entry.Action, entry.Reason = ActionSkip, "local and remote default branches diverged"
		return entry
	case RelationBehind:
	default:
		entry.Action, entry.Reason = ActionFailed, "default branch relation is unknown"
		return entry
	}
	if !observed.WorktreesKnown || !observed.OperationsKnown {
		entry.Action, entry.Reason = ActionFailed, "repository worktree state is unknown"
		return entry
	}
	if len(observed.Operations) > 0 {
		entry.Action, entry.Reason = ActionSkip, "active Git operation: "+string(observed.Operations[0])
		return entry
	}
	var checkedOut []gitops.WorktreeRegistration
	for _, worktree := range observed.Worktrees {
		if worktree.Branch == observed.LocalRef {
			checkedOut = append(checkedOut, worktree)
		}
	}
	if len(checkedOut) > 1 {
		entry.Action, entry.Reason = ActionSkip, "default branch is checked out in multiple worktrees"
		return entry
	}
	if len(checkedOut) == 0 {
		entry.Action = ActionUpdateRef
		return entry
	}
	entry.CheckoutPath = checkedOut[0].Path
	if filepath.Clean(checkedOut[0].Path) != filepath.Clean(observed.Identity.SourcePath) {
		entry.Action, entry.Reason = ActionSkip, "default branch is checked out at "+checkedOut[0].Path
		return entry
	}
	if !observed.CheckoutKnown || observed.Checkout.FullRef != observed.LocalRef || observed.Checkout.HeadOID != observed.LocalOID {
		entry.Action, entry.Reason = ActionFailed, "source checkout does not match the local default branch"
		return entry
	}
	if !observed.WorkingKnown {
		entry.Action, entry.Reason = ActionFailed, "source working tree state is unknown"
		return entry
	}
	if observed.WorkingTree.Dirty() {
		entry.Action, entry.Reason = ActionSkip, "source working tree has local changes"
		return entry
	}
	entry.Action = ActionUpdateCheckout
	return entry
}

type Executor struct {
	Git    Git
	Locker Locker
}

func (e Executor) Execute(ctx context.Context, plan Plan) (result Result, returnErr error) {
	if e.Git == nil || e.Locker == nil {
		return result, fmt.Errorf("Source Repository Update executor dependencies are incomplete")
	}
	var keys []string
	for _, repository := range plan.Repositories {
		if repository.Action == ActionUpdateCheckout || repository.Action == ActionUpdateRef {
			keys = append(keys, repository.GitCommonDir)
		}
	}
	var release func() error
	if len(keys) > 0 {
		var err error
		release, err = e.Locker.Acquire(ctx, keys)
		if err != nil {
			return result, fmt.Errorf("acquire source repository update locks: %w", err)
		}
		defer func() { returnErr = errors.Join(returnErr, release()) }()
	}
	for _, planned := range plan.Repositories {
		entry := RepositoryResult{
			ID: planned.ID, Path: planned.Path, From: planned.LocalOID, To: planned.TargetOID, Relation: planned.Relation,
		}
		switch planned.Action {
		case ActionUnchanged:
			entry.Status = StatusUnchanged
		case ActionSkip:
			entry.Status, entry.Err = StatusSkipped, errors.New(planned.Reason)
		case ActionFailed:
			entry.Status, entry.Err = StatusFailed, errors.New(planned.Reason)
		case ActionUpdateCheckout, ActionUpdateRef:
			entry = e.executeOne(ctx, planned)
		default:
			entry.Status, entry.Err = StatusFailed, fmt.Errorf("unknown planned action %q", planned.Action)
		}
		result.Repositories = append(result.Repositories, entry)
	}
	return result, nil
}

func (e Executor) executeOne(ctx context.Context, planned RepositoryPlan) RepositoryResult {
	result := RepositoryResult{ID: planned.ID, Path: planned.Path, Status: StatusFailed, From: planned.LocalOID, To: planned.TargetOID, Relation: planned.Relation}
	if err := e.revalidate(ctx, planned); err != nil {
		result.Err = fmt.Errorf("revalidate source repository: %w", err)
		return result
	}
	var err error
	if planned.Action == ActionUpdateCheckout {
		err = e.Git.FastForwardCheckout(ctx, planned.Path, planned.LocalRef, planned.LocalOID, planned.TargetOID)
	} else {
		err = e.Git.UpdateLocalBranch(ctx, planned.Path, planned.LocalRef, planned.LocalOID, planned.TargetOID)
	}
	if err != nil {
		observed, exists, inspectErr := e.Git.RefOID(ctx, planned.Path, planned.LocalRef)
		if inspectErr == nil && exists && observed == planned.TargetOID {
			if planned.Action == ActionUpdateRef {
				result.Status = StatusUpdated
				return result
			}
			checkout, checkoutErr := e.Git.InspectCheckout(ctx, planned.Path)
			status, statusErr := e.Git.WorkingTreeStatus(ctx, planned.Path)
			if checkoutErr == nil && statusErr == nil && checkout.FullRef == planned.LocalRef && checkout.HeadOID == planned.TargetOID && !status.Dirty() {
				result.Status = StatusUpdated
				return result
			}
			inspectErr = errors.Join(inspectErr, checkoutErr, statusErr)
			if checkoutErr == nil && (checkout.FullRef != planned.LocalRef || checkout.HeadOID != planned.TargetOID) {
				inspectErr = errors.Join(inspectErr, fmt.Errorf("checkout does not match updated branch"))
			}
			if statusErr == nil && status.Dirty() {
				inspectErr = errors.Join(inspectErr, fmt.Errorf("working tree has local changes after update"))
			}
		}
		result.Err = errors.Join(err, inspectErr)
		return result
	}
	observed, exists, err := e.Git.RefOID(ctx, planned.Path, planned.LocalRef)
	if err != nil || !exists || observed != planned.TargetOID {
		result.Err = fmt.Errorf("verify local default branch: expected %s, observed %s: %w", planned.TargetOID, observed, err)
		return result
	}
	result.Status = StatusUpdated
	return result
}

func (e Executor) revalidate(ctx context.Context, planned RepositoryPlan) error {
	identity, err := e.Git.InspectRepository(ctx, planned.Path)
	if err != nil {
		return err
	}
	if identity.CommonDir != planned.GitCommonDir {
		return fmt.Errorf("repository identity changed from %s to %s", planned.GitCommonDir, identity.CommonDir)
	}
	local, exists, err := e.Git.RefOID(ctx, identity.SourcePath, planned.LocalRef)
	if err != nil {
		return err
	}
	if !exists || local != planned.LocalOID {
		return fmt.Errorf("local default branch changed from %s to %s", planned.LocalOID, local)
	}
	if err := e.Git.CommitExists(ctx, identity.SourcePath, planned.TargetOID); err != nil {
		return fmt.Errorf("planned target is unavailable: %w", err)
	}
	ancestor, err := e.Git.IsAncestor(ctx, identity.SourcePath, planned.LocalOID, planned.TargetOID)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("planned target is no longer a fast-forward descendant")
	}
	worktrees, err := e.Git.ListWorktrees(ctx, identity.SourcePath)
	if err != nil {
		return err
	}
	var checkoutPath string
	for _, worktree := range worktrees {
		if worktree.Branch != planned.LocalRef {
			continue
		}
		if checkoutPath != "" {
			return fmt.Errorf("default branch is checked out in multiple worktrees")
		}
		checkoutPath = worktree.Path
	}
	for _, worktree := range worktrees {
		operations, operationErr := e.Git.ActiveOperations(ctx, worktree.Path)
		if operationErr != nil {
			return fmt.Errorf("inspect Git operations at %s: %w", worktree.Path, operationErr)
		}
		if len(operations) > 0 {
			return fmt.Errorf("active Git operation at %s: %s", worktree.Path, operations[0])
		}
	}
	if planned.Action == ActionUpdateRef {
		if checkoutPath != "" {
			return fmt.Errorf("default branch became checked out at %s", checkoutPath)
		}
		return nil
	}
	if filepath.Clean(checkoutPath) != filepath.Clean(identity.SourcePath) {
		return fmt.Errorf("default branch checkout moved to %s", checkoutPath)
	}
	checkout, err := e.Git.InspectCheckout(ctx, identity.SourcePath)
	if err != nil {
		return err
	}
	if checkout.FullRef != planned.LocalRef || checkout.HeadOID != planned.LocalOID {
		return fmt.Errorf("source checkout changed")
	}
	status, err := e.Git.WorkingTreeStatus(ctx, identity.SourcePath)
	if err != nil {
		return err
	}
	if status.Dirty() {
		return fmt.Errorf("source working tree has local changes")
	}
	return nil
}

func revalidateIdentity(ctx context.Context, git Git, configuredPath string, planned gitops.RepositoryIdentity) error {
	observed, err := git.InspectRepository(ctx, configuredPath)
	if err != nil {
		return err
	}
	if observed.SourcePath != planned.SourcePath || observed.CommonDir != planned.CommonDir {
		return fmt.Errorf("repository identity changed")
	}
	return nil
}

func inspectIdentities(ctx context.Context, git Git, configs []RepositoryConfig) (map[string]gitops.RepositoryIdentity, map[string]string) {
	identities := make(map[string]gitops.RepositoryIdentity, len(configs))
	problems := make(map[string]string)
	seenIDs := make(map[string]struct{}, len(configs))
	seenCommon := make(map[string]string, len(configs))
	for _, config := range configs {
		if _, exists := seenIDs[config.ID]; exists {
			problems[config.ID] = "repository ID was selected more than once"
			continue
		}
		seenIDs[config.ID] = struct{}{}
		if _, _, err := branchRefs(config.Remote, config.DefaultBranch); err != nil {
			problems[config.ID] = err.Error()
			continue
		}
		identity, err := git.InspectRepository(ctx, config.Path)
		if err != nil {
			problems[config.ID] = "inspect repository identity: " + err.Error()
			continue
		}
		if other, exists := seenCommon[identity.CommonDir]; exists {
			problems[config.ID] = "same Git repository is also configured as " + other
			problems[other] = "same Git repository is also configured as " + config.ID
			delete(identities, other)
			continue
		}
		seenCommon[identity.CommonDir] = config.ID
		identities[config.ID] = identity
	}
	return identities, problems
}

func identityKeys(identities map[string]gitops.RepositoryIdentity) []string {
	keys := make([]string, 0, len(identities))
	for _, identity := range identities {
		keys = append(keys, identity.CommonDir)
	}
	return unique(keys)
}

func relation(ctx context.Context, git Git, repo, local string, localKnown bool, remote string, remoteKnown bool) (Relation, error) {
	if !localKnown || !remoteKnown {
		return RelationMissing, nil
	}
	if local == remote {
		return RelationEqual, nil
	}
	behind, err := git.IsAncestor(ctx, repo, local, remote)
	if err != nil {
		return RelationUnknown, err
	}
	if behind {
		return RelationBehind, nil
	}
	ahead, err := git.IsAncestor(ctx, repo, remote, local)
	if err != nil {
		return RelationUnknown, err
	}
	if ahead {
		return RelationAhead, nil
	}
	return RelationDiverged, nil
}

func branchRefs(remote, branch string) (string, string, error) {
	remote = strings.TrimSpace(remote)
	branch = strings.TrimSpace(branch)
	if remote == "" {
		return "", "", fmt.Errorf("configured remote is empty")
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	branch = strings.TrimPrefix(branch, "refs/remotes/"+remote+"/")
	branch = strings.TrimPrefix(branch, remote+"/")
	if branch == "" || strings.HasPrefix(branch, "refs/") {
		return "", "", fmt.Errorf("configured default branch %q is not a local branch name", branch)
	}
	return "refs/heads/" + branch, "refs/remotes/" + remote + "/" + branch, nil
}

func sortedConfigs(configs []RepositoryConfig) []RepositoryConfig {
	result := append([]RepositoryConfig(nil), configs...)
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func appendProblem(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "; " + next
}
