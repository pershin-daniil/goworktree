package syncwork

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
)

type Relation string

const (
	RelationEqual        Relation = "equal"
	RelationContainsBase Relation = "contains-base"
	RelationBehindBase   Relation = "behind-base"
	RelationDiverged     Relation = "diverged"
	RelationUnknown      Relation = "unknown"
)

type RepositoryConfig struct {
	ID             string
	Remote         string
	BasePreference string
}

type RepositoryPlan struct {
	ID, SourcePath, GitCommonDir, Destination string
	BranchRef, PreHeadOID, Remote             string
	BaseRef, BaseOID                          string
	FetchedAt                                 time.Time
	Relation                                  Relation
	WorkingTree                               gitops.WorkingTreeStatus
	BlockedReason                             string
}

type Plan struct {
	WorkName string
	WorkID   string
	WorkRoot string
	BuiltAt  time.Time
	Repos    []RepositoryPlan
}

type RepositoryResult struct {
	ID     string
	Status gitops.SyncStatus
	From   string
	To     string
	Err    error
}

type Result struct {
	WorkName     string
	Repositories []RepositoryResult
}

func (r Result) Failed() bool {
	for _, repository := range r.Repositories {
		if repository.Status != gitops.SyncRebased && repository.Status != gitops.SyncUpToDate {
			return true
		}
	}
	return false
}

type Git interface {
	FetchRemote(context.Context, string, string) error
	ResolveBase(context.Context, string, string, string) (gitops.ResolvedBase, error)
	IsAncestor(context.Context, string, string, string) (bool, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	RebaseToOID(context.Context, string, string, string) gitops.SyncResult
}

type SystemGit struct{}

func (SystemGit) FetchRemote(ctx context.Context, repo, remote string) error {
	return gitops.FetchRemoteContext(ctx, repo, remote)
}
func (SystemGit) ResolveBase(ctx context.Context, repo, remote, preference string) (gitops.ResolvedBase, error) {
	return gitops.ResolveNewWorkBaseContext(ctx, repo, remote, "", preference)
}
func (SystemGit) IsAncestor(ctx context.Context, repo, older, newer string) (bool, error) {
	return gitops.IsAncestorContext(ctx, repo, older, newer)
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
func (SystemGit) RebaseToOID(ctx context.Context, path, branch, oid string) gitops.SyncResult {
	return gitops.RebaseWorktreeToOIDContext(ctx, path, branch, oid)
}

type Planner struct {
	Git Git
	Now func() time.Time
}

func (p Planner) Build(ctx context.Context, snapshot inspectwork.Snapshot, configs []RepositoryConfig) (Plan, error) {
	if p.Git == nil {
		return Plan{}, fmt.Errorf("Sync Work Git adapter is nil")
	}
	if snapshot.Manifest.Value == nil || snapshot.Manifest.State != inspectwork.MetadataValid {
		return Plan{}, fmt.Errorf("Work manifest is not valid; run Repair Work")
	}
	configByID := make(map[string]RepositoryConfig, len(configs))
	for _, config := range configs {
		configByID[config.ID] = config
	}
	plan := Plan{WorkName: snapshot.WorkName.String(), WorkID: snapshot.WorkID.String(), WorkRoot: snapshot.WorkRoot, BuiltAt: p.now().UTC()}
	for _, observed := range snapshot.Repositories {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		config, ok := configByID[observed.ID]
		entry := RepositoryPlan{
			ID: observed.ID, SourcePath: observed.Intent.SourcePath, GitCommonDir: observed.Intent.GitCommonDir,
			Destination: observed.Intent.Destination, BranchRef: observed.Intent.BranchRef,
			PreHeadOID: observed.Checkout.HeadOID, WorkingTree: observed.WorkingTree,
		}
		if !ok {
			entry.BlockedReason = "repository is missing from configuration"
		} else {
			entry.Remote = config.Remote
		}
		if entry.BlockedReason == "" {
			entry.BlockedReason = eligibilityProblem(observed)
		}
		if entry.BlockedReason == "" {
			if err := p.Git.FetchRemote(ctx, entry.SourcePath, entry.Remote); err != nil {
				entry.BlockedReason = "fetch failed: " + err.Error()
			} else if base, err := p.Git.ResolveBase(ctx, entry.SourcePath, entry.Remote, config.BasePreference); err != nil {
				entry.BlockedReason = "resolve base: " + err.Error()
			} else {
				entry.BaseRef, entry.BaseOID, entry.FetchedAt = base.FullRef, base.OID, p.now().UTC()
				entry.Relation, entry.BlockedReason = p.relation(ctx, entry.SourcePath, entry.PreHeadOID, entry.BaseOID)
			}
		}
		plan.Repos = append(plan.Repos, entry)
	}
	sort.Slice(plan.Repos, func(i, j int) bool { return plan.Repos[i].ID < plan.Repos[j].ID })
	return plan, nil
}

func (p Planner) relation(ctx context.Context, repo, head, base string) (Relation, string) {
	if head == base {
		return RelationEqual, ""
	}
	contains, err := p.Git.IsAncestor(ctx, repo, base, head)
	if err != nil {
		return RelationUnknown, err.Error()
	}
	if contains {
		return RelationContainsBase, ""
	}
	behind, err := p.Git.IsAncestor(ctx, repo, head, base)
	if err != nil {
		return RelationUnknown, err.Error()
	}
	if behind {
		return RelationBehindBase, ""
	}
	return RelationDiverged, ""
}

func eligibilityProblem(repository inspectwork.RepositorySnapshot) string {
	if !repository.SourceKnown || !repository.CheckoutKnown || !repository.WorkingTreeKnown || !repository.GitOperationsKnown {
		return "repository state cannot be proven; run Repair Work"
	}
	if len(repository.Problems) > 0 {
		return repository.Problems[0].Message
	}
	if repository.WorkingTree.DirtySubmodules > 0 {
		return "dirty submodule"
	}
	if repository.WorkingTree.Conflicted > 0 {
		return "unresolved index conflict"
	}
	if len(repository.GitOperations) > 0 {
		return "active Git operation: " + string(repository.GitOperations[0])
	}
	return ""
}

func (p Planner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

type Locker interface {
	AcquireExecution(context.Context, string, []string) (func() error, error)
}

type FileLocker struct{ Set lockops.Set }

func (l FileLocker) AcquireExecution(ctx context.Context, workID string, repositories []string) (func() error, error) {
	workLease, err := l.Set.Acquire(ctx, "works", []string{workID})
	if err != nil {
		return nil, err
	}
	repoLease, err := l.Set.Acquire(ctx, "repositories", repositories)
	if err != nil {
		return nil, errors.Join(err, workLease.Release())
	}
	return func() error { return errors.Join(repoLease.Release(), workLease.Release()) }, nil
}

type Executor struct {
	Git    Git
	Locker Locker
}

func (e Executor) Execute(ctx context.Context, plan Plan) (result Result, returnErr error) {
	if e.Git == nil || e.Locker == nil {
		return result, fmt.Errorf("Sync Work executor dependencies are incomplete")
	}
	identities := make([]string, 0, len(plan.Repos))
	for _, repository := range plan.Repos {
		if repository.GitCommonDir != "" {
			identities = append(identities, repository.GitCommonDir)
		}
	}
	release, err := e.Locker.AcquireExecution(ctx, plan.WorkID, unique(identities))
	if err != nil {
		return result, fmt.Errorf("acquire Sync Work locks: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, release()) }()
	result.WorkName = plan.WorkName
	for _, repository := range plan.Repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		observed := RepositoryResult{ID: repository.ID, Status: gitops.SyncFailed, From: repository.PreHeadOID}
		if repository.BlockedReason != "" {
			observed.Err = fmt.Errorf("blocked: %s", repository.BlockedReason)
			result.Repositories = append(result.Repositories, observed)
			continue
		}
		if err := e.revalidate(ctx, repository); err != nil {
			observed.Err = fmt.Errorf("state changed after plan: %w", err)
			result.Repositories = append(result.Repositories, observed)
			continue
		}
		if repository.Relation == RelationEqual || repository.Relation == RelationContainsBase {
			observed.Status, observed.To = gitops.SyncUpToDate, repository.PreHeadOID
		} else {
			synced := e.Git.RebaseToOID(ctx, repository.Destination, repository.BranchRef, repository.BaseOID)
			observed.Status, observed.From, observed.To, observed.Err = synced.Status, synced.From, synced.To, synced.Err
		}
		result.Repositories = append(result.Repositories, observed)
	}
	return result, nil
}

func (e Executor) revalidate(ctx context.Context, planned RepositoryPlan) error {
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir || checkout.FullRef != planned.BranchRef || checkout.HeadOID != planned.PreHeadOID {
		return fmt.Errorf("checkout identity, branch, or HEAD differs")
	}
	status, err := e.Git.WorkingTreeStatus(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if status != planned.WorkingTree {
		return fmt.Errorf("working tree changed")
	}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if len(operations) > 0 {
		return fmt.Errorf("active Git operation: %s", operations[0])
	}
	return nil
}

func unique(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func ShortOID(oid string) string {
	oid = strings.TrimSpace(oid)
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}
