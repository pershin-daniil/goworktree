package syncwork

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
	Recovery                                  bool
}

type Plan struct {
	WorkName        string
	WorkID          string
	WorkRoot        string
	BuiltAt         time.Time
	OperationRecord string
	Repos           []RepositoryPlan
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
	ContinueRebase(context.Context, string, string, string) gitops.SyncResult
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
func (SystemGit) ContinueRebase(ctx context.Context, path, branch, oid string) gitops.SyncResult {
	return gitops.ContinueRebaseContext(ctx, path, branch, oid)
}

type Planner struct {
	Git Git
	Now func() time.Time
}

func (p Planner) Build(ctx context.Context, snapshot inspectwork.Snapshot, controlRoot string, configs []RepositoryConfig) (Plan, error) {
	if p.Git == nil {
		return Plan{}, fmt.Errorf("Sync Work Git adapter is nil")
	}
	operationPath := filepath.Join(controlRoot, "operations", "sync-work", snapshot.WorkID.String()+".json")
	if recovered, ok, err := interruptedPlan(operationPath, snapshot.WorkID.String()); err != nil {
		return Plan{}, err
	} else if ok {
		return recovered, nil
	}
	if snapshot.Manifest.Value == nil || snapshot.Manifest.State != inspectwork.MetadataValid {
		return Plan{}, fmt.Errorf("Work manifest is not valid; run Repair Work")
	}
	configByID := make(map[string]RepositoryConfig, len(configs))
	for _, config := range configs {
		configByID[config.ID] = config
	}
	plan := Plan{
		WorkName: snapshot.WorkName.String(), WorkID: snapshot.WorkID.String(), WorkRoot: snapshot.WorkRoot,
		BuiltAt: p.now().UTC(), OperationRecord: operationPath,
	}
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

type checkpoint struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type operationRecord struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	Plan          Plan         `json:"plan"`
	Repositories  []checkpoint `json:"repositories"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

func interruptedPlan(path, workID string) (Plan, bool, error) {
	var record operationRecord
	if err := work.LoadJSON(path, &record); errors.Is(err, os.ErrNotExist) {
		return Plan{}, false, nil
	} else if err != nil {
		return Plan{}, false, fmt.Errorf("read Sync Work operation: %w", err)
	}
	if err := validateRecord(record, workID); err != nil {
		return Plan{}, false, err
	}
	recovered := record.Plan
	found := false
	for index := range record.Repositories {
		if record.Repositories[index].Status == string(gitops.SyncConflict) || record.Repositories[index].Status == "rebasing" {
			recovered.Repos[index].Recovery = true
			found = true
		}
	}
	return recovered, found, nil
}

func validateRecord(record operationRecord, workID string) error {
	if record.SchemaVersion != 1 || record.Kind != "sync-work" || record.Plan.WorkID != workID {
		return fmt.Errorf("Sync Work operation record is invalid")
	}
	if len(record.Repositories) != len(record.Plan.Repos) {
		return fmt.Errorf("Sync Work checkpoints do not match plan")
	}
	for index := range record.Repositories {
		if record.Repositories[index].ID != record.Plan.Repos[index].ID {
			return fmt.Errorf("Sync Work checkpoint %d does not match plan", index)
		}
	}
	return nil
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
	Now    func() time.Time
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
	record, err := e.loadOrStart(plan)
	if err != nil {
		return result, err
	}
	result.WorkName = plan.WorkName
	for index, repository := range plan.Repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		observed := RepositoryResult{ID: repository.ID, Status: gitops.SyncFailed, From: repository.PreHeadOID}
		if record.Repositories[index].Status == string(gitops.SyncRebased) || record.Repositories[index].Status == string(gitops.SyncUpToDate) {
			observed.Status = gitops.SyncStatus(record.Repositories[index].Status)
			result.Repositories = append(result.Repositories, observed)
			continue
		}
		if repository.Recovery {
			observed = e.recover(ctx, repository)
			record.Repositories[index].Status = string(observed.Status)
			if err := e.save(record); err != nil {
				return result, err
			}
			result.Repositories = append(result.Repositories, observed)
			continue
		}
		if repository.BlockedReason != "" {
			observed.Err = fmt.Errorf("blocked: %s", repository.BlockedReason)
			record.Repositories[index].Status = string(observed.Status)
			if err := e.save(record); err != nil {
				return result, err
			}
			result.Repositories = append(result.Repositories, observed)
			continue
		}
		if err := e.revalidate(ctx, repository); err != nil {
			observed.Err = fmt.Errorf("state changed after plan: %w", err)
			record.Repositories[index].Status = string(observed.Status)
			if err := e.save(record); err != nil {
				return result, err
			}
			result.Repositories = append(result.Repositories, observed)
			continue
		}
		if repository.Relation == RelationEqual || repository.Relation == RelationContainsBase {
			observed.Status, observed.To = gitops.SyncUpToDate, repository.PreHeadOID
		} else {
			record.Repositories[index].Status = "rebasing"
			if err := e.save(record); err != nil {
				return result, err
			}
			synced := e.Git.RebaseToOID(ctx, repository.Destination, repository.BranchRef, repository.BaseOID)
			observed.Status, observed.From, observed.To, observed.Err = synced.Status, synced.From, synced.To, synced.Err
		}
		record.Repositories[index].Status = string(observed.Status)
		if err := e.save(record); err != nil {
			return result, err
		}
		result.Repositories = append(result.Repositories, observed)
	}
	return result, nil
}

func (e Executor) recover(ctx context.Context, planned RepositoryPlan) RepositoryResult {
	result := RepositoryResult{ID: planned.ID, Status: gitops.SyncFailed, From: planned.PreHeadOID}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		result.Err = err
		return result
	}
	for _, operation := range operations {
		if operation == gitops.OperationRebase {
			synced := e.Git.ContinueRebase(ctx, planned.Destination, planned.BranchRef, planned.BaseOID)
			result.Status, result.From, result.To, result.Err = synced.Status, synced.From, synced.To, synced.Err
			return result
		}
	}
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil || checkout.FullRef != planned.BranchRef {
		result.Err = fmt.Errorf("recorded Sync rebase is no longer active and its checkout cannot be reconciled")
		return result
	}
	based, err := e.Git.IsAncestor(ctx, planned.SourcePath, planned.BaseOID, checkout.HeadOID)
	if err != nil || !based {
		result.Err = fmt.Errorf("recorded Sync rebase is no longer active and HEAD is not based on the planned commit")
		return result
	}
	result.Status, result.To = gitops.SyncRebased, checkout.HeadOID
	return result
}

func (e Executor) loadOrStart(plan Plan) (operationRecord, error) {
	record := operationRecord{SchemaVersion: 1, Kind: "sync-work", Plan: plan, UpdatedAt: e.now().UTC()}
	for _, repository := range plan.Repos {
		record.Repositories = append(record.Repositories, checkpoint{ID: repository.ID, Status: "pending"})
	}
	var current operationRecord
	err := work.LoadJSON(plan.OperationRecord, &current)
	if err == nil {
		if err := validateRecord(current, plan.WorkID); err != nil {
			return record, err
		}
		for _, checkpoint := range current.Repositories {
			if checkpoint.Status == string(gitops.SyncConflict) || checkpoint.Status == "rebasing" {
				return current, nil
			}
		}
		if err := work.ReplaceJSON(plan.OperationRecord, record, 0o600, nil); err != nil {
			return record, err
		}
		return record, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return record, err
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		return record, err
	}
	if err := work.CreateJSON(plan.OperationRecord, record, 0o600); err != nil {
		return record, err
	}
	return record, nil
}

func (e Executor) save(record operationRecord) error {
	record.UpdatedAt = e.now().UTC()
	return work.ReplaceJSON(record.Plan.OperationRecord, record, 0o600, nil)
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
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
