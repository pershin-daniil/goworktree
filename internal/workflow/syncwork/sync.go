package syncwork

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

const operationSchemaVersion = 3

type Relation string

const (
	RelationEqual        Relation = "equal"
	RelationContainsBase Relation = "contains-base"
	RelationBehindBase   Relation = "behind-base"
	RelationDiverged     Relation = "diverged"
	RelationUnknown      Relation = "unknown"
)

type RepositoryPhase string

const (
	PhasePending          RepositoryPhase = "pending"
	PhasePreserveIntent   RepositoryPhase = "preserve-intent"
	PhasePreserved        RepositoryPhase = "preserved"
	PhaseRebaseIntent     RepositoryPhase = "rebase-intent"
	PhaseRebaseConflict   RepositoryPhase = "rebase-conflict"
	PhaseRebased          RepositoryPhase = "rebased"
	PhaseRestoreIntent    RepositoryPhase = "restore-intent"
	PhaseCompleted        RepositoryPhase = "completed"
	PhaseFailedKnownState RepositoryPhase = "failed-known-state"
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
	InitialFingerprint                        string
	LegacyFingerprintUnavailable              bool `json:"legacy_fingerprint_unavailable,omitempty"`
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
	ID          string
	Destination string
	Status      gitops.SyncStatus
	From        string
	To          string
	Err         error
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
	WorktreeFingerprint(context.Context, string) (string, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	RebaseToOID(context.Context, string, string, string) gitops.SyncResult
	ContinueRebase(context.Context, string, string, string) gitops.SyncResult
	Stashes(context.Context, string) ([]gitops.StashEntry, error)
	Preserve(context.Context, string, string, string, []gitops.StashEntry) (string, error)
	RecoveryOID(context.Context, string, string) (string, bool, error)
	ApplyRecovery(context.Context, string, string) error
	DeleteRecoveryRef(context.Context, string, string, string) error
	AbortAndReset(context.Context, string, string, string) error
	RecoveryRefs(context.Context, string, string) ([]gitops.RecoveryRef, error)
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
func (SystemGit) WorktreeFingerprint(ctx context.Context, path string) (string, error) {
	return gitops.WorktreeFingerprintContext(ctx, path)
}
func (SystemGit) ActiveOperations(ctx context.Context, path string) ([]gitops.ActiveOperation, error) {
	return gitops.ActiveOperationsContext(ctx, path)
}
func (SystemGit) RebaseToOID(ctx context.Context, path, branch, oid string) gitops.SyncResult {
	return gitops.RebaseCleanWorktreeToOIDContext(ctx, path, branch, oid)
}
func (SystemGit) ContinueRebase(ctx context.Context, path, branch, oid string) gitops.SyncResult {
	return gitops.ContinueRebaseContext(ctx, path, branch, oid)
}
func (SystemGit) Stashes(ctx context.Context, repo string) ([]gitops.StashEntry, error) {
	return gitops.StashEntriesContext(ctx, repo)
}
func (SystemGit) Preserve(ctx context.Context, repo, ref, marker string, baseline []gitops.StashEntry) (string, error) {
	return gitops.PreserveWorktreeContext(ctx, repo, ref, marker, baseline)
}
func (SystemGit) RecoveryOID(ctx context.Context, repo, ref string) (string, bool, error) {
	return gitops.RefOIDContext(ctx, repo, ref)
}
func (SystemGit) ApplyRecovery(ctx context.Context, repo, oid string) error {
	return gitops.ApplyRecoveryContext(ctx, repo, oid)
}
func (SystemGit) DeleteRecoveryRef(ctx context.Context, repo, ref, oid string) error {
	return gitops.DeleteRecoveryRefContext(ctx, repo, ref, oid)
}
func (SystemGit) AbortAndReset(ctx context.Context, repo, branch, oid string) error {
	return gitops.AbortRebaseAndResetContext(ctx, repo, branch, oid)
}
func (SystemGit) RecoveryRefs(ctx context.Context, repo, prefix string) ([]gitops.RecoveryRef, error) {
	return gitops.ListRecoveryRefsContext(ctx, repo, prefix)
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
	recoveryPrefix := "refs/goworktree/recovery/" + snapshot.WorkID.String() + "/"
	for _, observed := range snapshot.Repositories {
		if !observed.SourceKnown {
			continue
		}
		refs, err := p.Git.RecoveryRefs(ctx, observed.Intent.SourcePath, recoveryPrefix)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: inspect private Sync recovery refs: %w", observed.ID, err)
		}
		if len(refs) > 0 {
			return Plan{}, fmt.Errorf("repository %s has private recovery ref %s without an unfinished Sync record; preserve it and repair the operation state before fetching", observed.ID, refs[0].Ref)
		}
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
			fingerprint, err := p.Git.WorktreeFingerprint(ctx, entry.Destination)
			if err != nil {
				entry.BlockedReason = "fingerprint working tree: " + err.Error()
			} else {
				entry.InitialFingerprint = fingerprint
			}
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
	ID                  string              `json:"id"`
	Phase               RepositoryPhase     `json:"phase,omitempty"`
	ResultStatus        gitops.SyncStatus   `json:"result_status,omitempty"`
	From                string              `json:"from,omitempty"`
	To                  string              `json:"to,omitempty"`
	RecoveryRef         string              `json:"recovery_ref,omitempty"`
	RecoveryOID         string              `json:"recovery_oid,omitempty"`
	Marker              string              `json:"marker,omitempty"`
	OriginalStash       []gitops.StashEntry `json:"original_stash,omitempty"`
	LastProblem         string              `json:"last_problem,omitempty"`
	Rollback            bool                `json:"rollback,omitempty"`
	OwnedFingerprint    string              `json:"owned_fingerprint,omitempty"`
	RestoredFingerprint string              `json:"restored_fingerprint,omitempty"`

	// Schema-v1 migration field.
	Status string `json:"status,omitempty"`
}

type operationRecord struct {
	SchemaVersion int          `json:"schema_version"`
	OperationID   string       `json:"operation_id,omitempty"`
	Kind          string       `json:"kind"`
	Plan          Plan         `json:"plan"`
	Repositories  []checkpoint `json:"repositories"`
	CreatedAt     time.Time    `json:"created_at,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

func interruptedPlan(path, workID string) (Plan, bool, error) {
	record, err := loadRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return Plan{}, false, nil
	}
	if err != nil {
		return Plan{}, false, fmt.Errorf("read Sync Work operation: %w", err)
	}
	record, migrated, err := migrateAndValidate(record, workID)
	if err != nil {
		return Plan{}, false, err
	}
	if migrated {
		if err := work.ReplaceJSON(path, record, 0o600, nil); err != nil {
			return Plan{}, false, fmt.Errorf("persist migrated Sync Work operation: %w", err)
		}
	}
	if terminal(record) {
		return Plan{}, false, nil
	}
	recovered := record.Plan
	for index := range recovered.Repos {
		recovered.Repos[index].Recovery = record.Repositories[index].Phase != PhasePending
	}
	return recovered, true, nil
}

// OperationTerminal classifies an active Sync record without mutating it.
func OperationTerminal(path, workID string) (exists, isTerminal bool, err error) {
	record, err := loadRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	record, _, err = migrateAndValidate(record, workID)
	if err != nil {
		return true, false, err
	}
	return true, terminal(record), nil
}

func validateRecord(record operationRecord, workID string) error {
	if record.SchemaVersion != operationSchemaVersion || record.Kind != "sync-work" || record.Plan.WorkID != workID {
		return fmt.Errorf("Sync Work operation record is invalid")
	}
	if len(record.OperationID) != 32 {
		return fmt.Errorf("Sync Work operation ID is invalid")
	}
	if _, err := hex.DecodeString(record.OperationID); err != nil {
		return fmt.Errorf("Sync Work operation ID is invalid")
	}
	if len(record.Repositories) != len(record.Plan.Repos) {
		return fmt.Errorf("Sync Work checkpoints do not match plan")
	}
	for index := range record.Repositories {
		checkpoint := record.Repositories[index]
		planned := record.Plan.Repos[index]
		if checkpoint.ID != record.Plan.Repos[index].ID {
			return fmt.Errorf("Sync Work checkpoint %d does not match plan", index)
		}
		switch checkpoint.Phase {
		case PhasePending, PhasePreserveIntent, PhasePreserved, PhaseRebaseIntent, PhaseRebaseConflict,
			PhaseRebased, PhaseRestoreIntent, PhaseCompleted, PhaseFailedKnownState:
		default:
			return fmt.Errorf("Sync Work checkpoint %d has invalid phase %q", index, checkpoint.Phase)
		}
		if checkpoint.RecoveryOID != "" && checkpoint.RecoveryRef == "" {
			return fmt.Errorf("Sync Work checkpoint %d has a recovery OID without a ref", index)
		}
		if checkpoint.RecoveryRef != "" {
			if checkpoint.RecoveryRef != recoveryRef(record.Plan.WorkID, record.OperationID, planned.ID) ||
				checkpoint.Marker != recoveryMarker(record.Plan.WorkID, record.OperationID, planned.ID) {
				return fmt.Errorf("Sync Work checkpoint %d has invalid recovery ownership", index)
			}
		}
		if planned.BlockedReason == "" && planned.InitialFingerprint == "" && !planned.LegacyFingerprintUnavailable {
			return fmt.Errorf("Sync Work checkpoint %d is missing the initial working-tree fingerprint", index)
		}
		needsRecovery := workingTreeDirty(planned.WorkingTree) && checkpoint.Phase != PhasePending && checkpoint.Phase != PhaseCompleted
		if needsRecovery && (checkpoint.RecoveryRef == "" || checkpoint.Marker == "") {
			return fmt.Errorf("Sync Work checkpoint %d is missing recovery ownership", index)
		}
		if needsRecovery && checkpoint.Phase != PhasePreserveIntent && checkpoint.RecoveryOID == "" {
			return fmt.Errorf("Sync Work checkpoint %d is missing a recovery OID", index)
		}
	}
	return nil
}

func migrateAndValidate(record operationRecord, workID string) (operationRecord, bool, error) {
	if record.SchemaVersion == operationSchemaVersion {
		return record, false, validateRecord(record, workID)
	}
	if record.Kind != "sync-work" || record.Plan.WorkID != workID || len(record.Repositories) != len(record.Plan.Repos) {
		return record, false, fmt.Errorf("Sync Work operation record is invalid")
	}
	if record.SchemaVersion == 2 {
		for index := range record.Plan.Repos {
			planned, checkpoint := &record.Plan.Repos[index], record.Repositories[index]
			if planned.InitialFingerprint == "" {
				planned.LegacyFingerprintUnavailable = true
			}
			if planned.LegacyFingerprintUnavailable && workingTreeDirty(planned.WorkingTree) && checkpoint.Phase != PhaseCompleted {
				return record, false, fmt.Errorf("unfinished dirty schema-v2 Sync for repository %s has no exact initial fingerprint; preserve the recovery ref and repair the operation manually", planned.ID)
			}
		}
		record.SchemaVersion = operationSchemaVersion
		return record, true, validateRecord(record, workID)
	}
	if record.SchemaVersion != 1 {
		return record, false, fmt.Errorf("Sync Work operation record is invalid")
	}
	operationID, err := randomOperationID()
	if err != nil {
		return record, false, err
	}
	record.SchemaVersion, record.OperationID = operationSchemaVersion, operationID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = record.Plan.BuiltAt
	}
	for index := range record.Repositories {
		checkpoint, planned := &record.Repositories[index], &record.Plan.Repos[index]
		planned.LegacyFingerprintUnavailable = planned.InitialFingerprint == ""
		if checkpoint.ID != planned.ID {
			return record, false, fmt.Errorf("Sync Work checkpoint %d does not match plan", index)
		}
		switch checkpoint.Status {
		case "", "pending":
			checkpoint.Phase = PhasePending
		case "rebasing", string(gitops.SyncConflict):
			if workingTreeDirty(planned.WorkingTree) {
				return record, false, fmt.Errorf("unfinished dirty schema-v1 Sync for repository %s has no provable recovery OID; preserve changes manually before continuing", planned.ID)
			}
			checkpoint.Phase = PhaseRebaseConflict
		case string(gitops.SyncFailed):
			if workingTreeDirty(planned.WorkingTree) {
				return record, false, fmt.Errorf("unfinished dirty schema-v1 Sync for repository %s has no provable recovery OID; preserve changes manually before continuing", planned.ID)
			}
			checkpoint.Phase, checkpoint.ResultStatus = PhaseCompleted, gitops.SyncFailed
		case string(gitops.SyncRebased), string(gitops.SyncUpToDate), string(gitops.SyncRolledBack):
			checkpoint.Phase, checkpoint.ResultStatus = PhaseCompleted, gitops.SyncStatus(checkpoint.Status)
		default:
			return record, false, fmt.Errorf("schema-v1 Sync checkpoint %s has unknown status %q", planned.ID, checkpoint.Status)
		}
		checkpoint.Status = ""
	}
	return record, true, validateRecord(record, workID)
}

func terminal(record operationRecord) bool {
	for _, checkpoint := range record.Repositories {
		if checkpoint.Phase != PhaseCompleted {
			return false
		}
	}
	return true
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
	Git          Git
	Locker       Locker
	Now          func() time.Time
	OperationID  func() (string, error)
	ReconcileFor time.Duration
	Fault        func(string) error
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
	result.WorkName = record.Plan.WorkName
	for index := range record.Plan.Repos {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		observed := e.processRepository(ctx, &record, index)
		result.Repositories = append(result.Repositories, observed)
	}
	return result, nil
}

func (e Executor) processRepository(ctx context.Context, record *operationRecord, index int) RepositoryResult {
	planned, checkpoint := record.Plan.Repos[index], &record.Repositories[index]
	result := RepositoryResult{
		ID: planned.ID, Destination: planned.Destination, Status: gitops.SyncFailed,
		From: planned.PreHeadOID, To: checkpoint.To,
	}
	if checkpoint.Phase == PhaseCompleted {
		result.Status, result.From, result.To = checkpoint.ResultStatus, checkpoint.From, checkpoint.To
		if checkpoint.LastProblem != "" && result.Status != gitops.SyncRebased && result.Status != gitops.SyncUpToDate {
			result.Err = errors.New(checkpoint.LastProblem)
		}
		return result
	}
	if planned.BlockedReason != "" && checkpoint.Phase == PhasePending {
		return e.complete(record, index, result, gitops.SyncFailed, planned.PreHeadOID, "blocked: "+planned.BlockedReason)
	}
	if checkpoint.Phase == PhaseFailedKnownState {
		if workingTreeDirty(planned.WorkingTree) && checkpoint.RecoveryRef != "" {
			return e.rollbackAndRestore(record, index, result, errors.New("resume failed-known-state Sync cleanup"))
		}
		return e.reconcileClean(record, index, result)
	}
	if checkpoint.Phase == PhaseRebaseConflict {
		if workingTreeDirty(planned.WorkingTree) {
			return e.rollbackAndRestore(record, index, result, errors.New("dirty Sync rebase conflicted"))
		}
		return e.continueCleanRebase(ctx, record, index, result)
	}
	if checkpoint.Phase == PhaseRebaseIntent {
		if reconciled, done := e.reconcileRebaseIntent(record, index, result); done {
			return reconciled
		}
	}
	if checkpoint.Phase == PhasePending {
		if err := e.revalidate(ctx, planned); err != nil {
			return e.complete(record, index, result, gitops.SyncFailed, planned.PreHeadOID, "state changed after plan: "+err.Error())
		}
		if planned.Relation == RelationEqual || planned.Relation == RelationContainsBase {
			return e.complete(record, index, result, gitops.SyncUpToDate, planned.PreHeadOID, "")
		}
		if workingTreeDirty(planned.WorkingTree) {
			baseline, err := e.Git.Stashes(ctx, planned.SourcePath)
			if err != nil {
				result.Err = err
				return result
			}
			checkpoint.Phase = PhasePreserveIntent
			checkpoint.RecoveryRef = recoveryRef(record.Plan.WorkID, record.OperationID, planned.ID)
			checkpoint.Marker = recoveryMarker(record.Plan.WorkID, record.OperationID, planned.ID)
			checkpoint.OriginalStash = baseline
			if err := e.save(*record); err != nil {
				result.Err = fmt.Errorf("checkpoint preservation intent: %w", err)
				return result
			}
			if err := e.inject("preserve-intent:" + planned.ID); err != nil {
				result.Err = err
				return result
			}
		} else {
			checkpoint.Phase = PhaseRebaseIntent
			if err := e.save(*record); err != nil {
				result.Err = fmt.Errorf("checkpoint rebase intent: %w", err)
				return result
			}
		}
	}
	if checkpoint.Phase == PhasePreserveIntent {
		if err := e.revalidatePreservationIntent(ctx, planned); err != nil {
			result.Err = fmt.Errorf("reconcile preservation intent: %w", err)
			checkpoint.LastProblem = result.Err.Error()
			_ = e.save(*record)
			return result
		}
		mutationCtx, cancel := syncMutationContext(ctx)
		oid, err := e.Git.Preserve(mutationCtx, planned.Destination, checkpoint.RecoveryRef, checkpoint.Marker, checkpoint.OriginalStash)
		cancel()
		if err != nil {
			result.Err = fmt.Errorf("preserve repository %s changes: %w", planned.ID, err)
			checkpoint.LastProblem = result.Err.Error()
			_ = e.save(*record)
			return result
		}
		if err := e.inject("preserve-mutation:" + planned.ID); err != nil {
			result.Err = err
			return result
		}
		fingerprint, err := e.cleanFingerprintAtHead(planned, planned.PreHeadOID)
		if err != nil {
			result.Err = fmt.Errorf("verify preserved working tree: %w", err)
			checkpoint.LastProblem = result.Err.Error()
			_ = e.save(*record)
			return result
		}
		checkpoint.RecoveryOID, checkpoint.OwnedFingerprint = oid, fingerprint
		checkpoint.Phase, checkpoint.LastProblem = PhasePreserved, ""
		if err := e.save(*record); err != nil {
			result.Err = fmt.Errorf("checkpoint preserved changes: %w", err)
			return result
		}
		if err := e.inject("preserved:" + planned.ID); err != nil {
			result.Err = err
			return result
		}
		if ctx.Err() != nil {
			result.Err = ctx.Err()
			return result
		}
	}
	if checkpoint.Phase == PhasePreserved {
		if err := e.verifyRecovery(record, index); err != nil {
			return e.failKnown(record, index, result, err)
		}
		if err := e.revalidatePreserved(ctx, planned, *checkpoint); err != nil {
			return e.failKnown(record, index, result, err)
		}
		checkpoint.Phase = PhaseRebaseIntent
		if err := e.save(*record); err != nil {
			result.Err = fmt.Errorf("checkpoint rebase intent: %w", err)
			return result
		}
		if err := e.inject("rebase-intent:" + planned.ID); err != nil {
			result.Err = err
			return result
		}
	}
	if checkpoint.Phase == PhaseRebaseIntent {
		if err := e.verifyRebaseStart(ctx, record, index); err != nil {
			return e.failKnown(record, index, result, fmt.Errorf("revalidate immediately before rebase: %w", err))
		}
		mutationCtx, cancel := syncMutationContext(ctx)
		synced := e.Git.RebaseToOID(mutationCtx, planned.Destination, planned.BranchRef, planned.BaseOID)
		cancel()
		if err := e.inject("rebase-mutation:" + planned.ID); err != nil {
			result.Err = err
			return result
		}
		result.Status, result.From, result.To, result.Err = synced.Status, synced.From, synced.To, synced.Err
		switch synced.Status {
		case gitops.SyncRebased, gitops.SyncUpToDate:
			fingerprint, err := e.cleanFingerprintAtHead(planned, synced.To)
			if err != nil {
				return e.failKnown(record, index, result, fmt.Errorf("verify rebased working tree: %w", err))
			}
			checkpoint.Phase, checkpoint.From, checkpoint.To = PhaseRebased, synced.From, synced.To
			checkpoint.OwnedFingerprint = fingerprint
			if err := e.save(*record); err != nil {
				result.Err = fmt.Errorf("checkpoint completed rebase: %w", err)
				return result
			}
			if err := e.inject("rebased:" + planned.ID); err != nil {
				result.Err = err
				return result
			}
		case gitops.SyncConflict:
			if workingTreeDirty(planned.WorkingTree) {
				if err := e.recordOwnedCurrentState(record, index); err != nil {
					return e.failKnown(record, index, result, errors.Join(synced.Err, err))
				}
			}
			checkpoint.Phase, checkpoint.LastProblem = PhaseRebaseConflict, errorText(synced.Err)
			if err := e.save(*record); err != nil {
				result.Err = errors.Join(result.Err, err)
			}
			if workingTreeDirty(planned.WorkingTree) {
				return e.rollbackAndRestore(record, index, result, synced.Err)
			}
			return result
		default:
			if workingTreeDirty(planned.WorkingTree) {
				if err := e.recordOwnedCurrentState(record, index); err != nil {
					return e.failKnown(record, index, result, errors.Join(synced.Err, err))
				}
				return e.rollbackAndRestore(record, index, result, synced.Err)
			}
			return e.reconcileClean(record, index, result)
		}
	}
	if checkpoint.Phase == PhaseRebased || checkpoint.Phase == PhaseRestoreIntent {
		if !workingTreeDirty(planned.WorkingTree) {
			return e.complete(record, index, result, gitops.SyncRebased, checkpoint.To, "")
		}
		return e.restoreAfterRebase(record, index, result)
	}
	return result
}

func (e Executor) restoreAfterRebase(record *operationRecord, index int, result RepositoryResult) RepositoryResult {
	planned, checkpoint := record.Plan.Repos[index], &record.Repositories[index]
	if checkpoint.Rollback {
		return e.rollbackAndRestore(record, index, result, errors.New(checkpoint.LastProblem))
	}
	if checkpoint.Phase == PhaseRestoreIntent {
		if checkpoint.RestoredFingerprint != "" {
			if err := e.verifyRestored(planned, checkpoint.To, checkpoint.RestoredFingerprint); err != nil {
				return e.failKnown(record, index, result, fmt.Errorf("verified recovery state changed; recovery ref is retained: %w", err))
			}
			if err := e.deleteRecoveryIfPresent(record, index); err != nil {
				if isInjectedFault(err) {
					result.Err = err
					return result
				}
				return e.failKnown(record, index, result, err)
			}
			return e.complete(record, index, result, gitops.SyncRebased, checkpoint.To, "")
		}
		if err := e.verifyOwnedState(planned, *checkpoint, checkpoint.To); err != nil {
			return e.failKnown(record, index, result, fmt.Errorf("recovery apply may have completed before its exact result was recorded; recovery ref is retained: %w", err))
		}
	}
	if err := e.verifyRecovery(record, index); err != nil {
		return e.failKnown(record, index, result, err)
	}
	if checkpoint.Phase != PhaseRestoreIntent {
		checkpoint.Phase = PhaseRestoreIntent
		checkpoint.Rollback = false
		if err := e.save(*record); err != nil {
			result.Err = err
			return result
		}
		if err := e.inject("restore-intent:" + planned.ID); err != nil {
			result.Err = err
			return result
		}
	}
	mutationCtx, cancel := e.reconciliationContext()
	err := e.Git.ApplyRecovery(mutationCtx, planned.Destination, checkpoint.RecoveryOID)
	cancel()
	if faultErr := e.inject("restore-mutation:" + planned.ID); faultErr != nil {
		result.Err = faultErr
		return result
	}
	if err != nil {
		cause := fmt.Errorf("restore changes after rebase: %w", err)
		if checkpointErr := e.checkpointImmediateRollbackState(record, index, cause); checkpointErr != nil {
			return e.failKnown(record, index, result, errors.Join(cause, checkpointErr))
		}
		return e.rollbackAndRestore(record, index, result, cause)
	}
	fingerprint, err := e.appliedRecoveryFingerprint(planned, checkpoint.To)
	if err != nil {
		cause := fmt.Errorf("verify restored changes: %w", err)
		if checkpointErr := e.checkpointImmediateRollbackState(record, index, cause); checkpointErr != nil {
			return e.failKnown(record, index, result, errors.Join(cause, checkpointErr))
		}
		return e.rollbackAndRestore(record, index, result, cause)
	}
	checkpoint.RestoredFingerprint = fingerprint
	if err := e.save(*record); err != nil {
		result.Err = fmt.Errorf("checkpoint restored working tree: %w", err)
		return result
	}
	if err := e.inject("restore-verified:" + planned.ID); err != nil {
		result.Err = err
		return result
	}
	if err := e.verifyRestored(planned, checkpoint.To, checkpoint.RestoredFingerprint); err != nil {
		return e.failKnown(record, index, result, fmt.Errorf("restored working tree changed before recovery cleanup: %w", err))
	}
	if err := e.deleteRecoveryIfPresent(record, index); err != nil {
		if isInjectedFault(err) {
			result.Err = err
			return result
		}
		return e.failKnown(record, index, result, fmt.Errorf("delete verified recovery ref: %w", err))
	}
	return e.complete(record, index, result, gitops.SyncRebased, checkpoint.To, "")
}

func (e Executor) rollbackAndRestore(record *operationRecord, index int, result RepositoryResult, cause error) RepositoryResult {
	planned, checkpoint := record.Plan.Repos[index], &record.Repositories[index]
	if checkpoint.Phase != PhaseRestoreIntent || !checkpoint.Rollback {
		checkpoint.Phase, checkpoint.Rollback, checkpoint.To = PhaseRestoreIntent, true, planned.PreHeadOID
		checkpoint.RestoredFingerprint = ""
		checkpoint.LastProblem = errorText(cause)
		if err := e.save(*record); err != nil {
			result.Err = errors.Join(cause, err)
			return result
		}
	}
	if planned.InitialFingerprint != "" && e.verifyRestored(planned, planned.PreHeadOID, planned.InitialFingerprint) == nil {
		if checkpoint.RestoredFingerprint != planned.InitialFingerprint {
			checkpoint.RestoredFingerprint = planned.InitialFingerprint
			if err := e.save(*record); err != nil {
				result.Err = fmt.Errorf("checkpoint verified rollback: %w", err)
				return result
			}
		}
		if err := e.deleteRecoveryIfPresent(record, index); err != nil {
			if isInjectedFault(err) {
				result.Err = err
				return result
			}
			return e.failKnown(record, index, result, err)
		}
		return e.complete(record, index, result, gitops.SyncRolledBack, planned.PreHeadOID, checkpoint.LastProblem)
	}
	if err := e.verifyRecovery(record, index); err != nil {
		return e.failKnown(record, index, result, errors.Join(cause, err))
	}
	if err := e.verifyOwnedState(planned, *checkpoint, ""); err != nil {
		return e.failKnown(record, index, result, fmt.Errorf("%v; rollback state changed and recovery ref is retained: %w", cause, err))
	}
	cleanupCtx, cancel := e.reconciliationContext()
	err := e.Git.AbortAndReset(cleanupCtx, planned.Destination, planned.BranchRef, planned.PreHeadOID)
	cancel()
	if err == nil {
		err = e.recordOwnedCurrentState(record, index)
	}
	if err == nil {
		cleanupCtx, cancel = e.reconciliationContext()
		err = e.Git.ApplyRecovery(cleanupCtx, planned.Destination, checkpoint.RecoveryOID)
		cancel()
	}
	if err != nil {
		return e.failKnown(record, index, result, fmt.Errorf("%v; rollback may have partially changed the working tree and recovery is retained: %w", cause, err))
	}
	err = e.verifyRestored(planned, planned.PreHeadOID, planned.InitialFingerprint)
	if err != nil {
		return e.failKnown(record, index, result, fmt.Errorf("%v; rollback recovery is retained: %w", cause, err))
	}
	checkpoint.RestoredFingerprint = planned.InitialFingerprint
	if err := e.save(*record); err != nil {
		result.Err = fmt.Errorf("%v; checkpoint verified rollback before recovery cleanup: %w", cause, err)
		return result
	}
	if err := e.deleteRecoveryIfPresent(record, index); err != nil {
		if isInjectedFault(err) {
			result.Err = err
			return result
		}
		return e.failKnown(record, index, result, fmt.Errorf("%v; restored original tree but recovery ref cleanup failed: %w", cause, err))
	}
	return e.complete(record, index, result, gitops.SyncRolledBack, planned.PreHeadOID, errorText(cause))
}

func (e Executor) continueCleanRebase(ctx context.Context, record *operationRecord, index int, result RepositoryResult) RepositoryResult {
	planned := record.Plan.Repos[index]
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return e.failKnown(record, index, result, err)
	}
	for _, operation := range operations {
		if operation == gitops.OperationRebase {
			mutationCtx, cancel := syncMutationContext(ctx)
			synced := e.Git.ContinueRebase(mutationCtx, planned.Destination, planned.BranchRef, planned.BaseOID)
			cancel()
			if synced.Status == gitops.SyncConflict {
				record.Repositories[index].LastProblem = errorText(synced.Err)
				_ = e.save(*record)
				result.Status, result.From, result.To, result.Err = synced.Status, synced.From, synced.To, synced.Err
				return result
			}
			if synced.Status == gitops.SyncRebased || synced.Status == gitops.SyncUpToDate {
				return e.complete(record, index, result, synced.Status, synced.To, "")
			}
			return e.reconcileClean(record, index, result)
		}
	}
	return e.reconcileClean(record, index, result)
}

func (e Executor) reconcileRebaseIntent(record *operationRecord, index int, result RepositoryResult) (RepositoryResult, bool) {
	planned, checkpoint := record.Plan.Repos[index], &record.Repositories[index]
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return e.failKnown(record, index, result, err), true
	}
	for _, operation := range operations {
		if operation == gitops.OperationRebase {
			if workingTreeDirty(planned.WorkingTree) {
				return e.failKnown(record, index, result, errors.New("interrupted dirty rebase has no durable operation-owned fingerprint; recovery ref is retained")), true
			}
			checkpoint.Phase = PhaseRebaseConflict
			_ = e.save(*record)
			result.Status, result.Err = gitops.SyncConflict, errors.New("recorded Sync rebase is still active; resolve and Resume")
			return result, true
		}
	}
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil || checkout.FullRef != planned.BranchRef {
		return e.failKnown(record, index, result, fmt.Errorf("rebase intent checkout cannot be reconciled: %w", err)), true
	}
	if checkout.HeadOID == planned.PreHeadOID {
		if err := e.verifyRetryablePreRebaseState(ctx, planned, *checkpoint); err != nil {
			return e.failKnown(record, index, result, fmt.Errorf("rebase intent state cannot be retried: %w", err)), true
		}
		if workingTreeDirty(planned.WorkingTree) {
			if recoveryErr := e.verifyRecovery(record, index); recoveryErr != nil {
				return e.failKnown(record, index, result, recoveryErr), true
			}
		}
		return result, false
	}
	based, err := e.Git.IsAncestor(ctx, planned.SourcePath, planned.BaseOID, checkout.HeadOID)
	if err == nil && based {
		if workingTreeDirty(planned.WorkingTree) {
			return e.failKnown(record, index, result, errors.New("rebase completed before its exact operation-owned fingerprint was durably recorded; recovery ref is retained")), true
		}
		fingerprint, fingerprintErr := e.cleanFingerprintAtHead(planned, checkout.HeadOID)
		if fingerprintErr != nil {
			return e.failKnown(record, index, result, fmt.Errorf("reconciled rebase working tree is not clean: %w", fingerprintErr)), true
		}
		checkpoint.Phase, checkpoint.From, checkpoint.To = PhaseRebased, planned.PreHeadOID, checkout.HeadOID
		checkpoint.OwnedFingerprint = fingerprint
		if saveErr := e.save(*record); saveErr != nil {
			result.Err = saveErr
			return result, true
		}
		if workingTreeDirty(planned.WorkingTree) {
			return e.restoreAfterRebase(record, index, result), true
		}
		return e.complete(record, index, result, gitops.SyncRebased, checkout.HeadOID, ""), true
	}
	return e.failKnown(record, index, result, errors.New("rebase intent ended at an unexpected HEAD")), true
}

func (e Executor) reconcileClean(record *operationRecord, index int, result RepositoryResult) RepositoryResult {
	planned, checkpoint := record.Plan.Repos[index], &record.Repositories[index]
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return e.failKnown(record, index, result, err)
	}
	for _, operation := range operations {
		if operation == gitops.OperationRebase {
			record.Repositories[index].Phase = PhaseRebaseConflict
			record.Repositories[index].LastProblem = "rebase requires manual conflict resolution"
			_ = e.save(*record)
			result.Status, result.Err = gitops.SyncConflict, errors.New(record.Repositories[index].LastProblem)
			return result
		}
	}
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil || checkout.FullRef != planned.BranchRef {
		return e.failKnown(record, index, result, fmt.Errorf("checkout cannot be reconciled after rebase: %w", err))
	}
	based, err := e.Git.IsAncestor(ctx, planned.SourcePath, planned.BaseOID, checkout.HeadOID)
	if err == nil && based {
		return e.complete(record, index, result, gitops.SyncRebased, checkout.HeadOID, "")
	}
	if checkout.HeadOID == planned.PreHeadOID {
		if retryErr := e.verifyRetryablePreRebaseState(ctx, planned, *checkpoint); retryErr == nil {
			problem := checkpoint.LastProblem
			checkpoint.Phase = PhaseRebaseIntent
			checkpoint.LastProblem = ""
			if saveErr := e.save(*record); saveErr != nil {
				result.Err = errors.Join(result.Err, saveErr)
				return result
			}
			result.Status = gitops.SyncFailed
			if result.Err == nil {
				if problem == "" {
					problem = "previous clean rebase attempt failed before mutation; retry Sync Work"
				}
				result.Err = errors.New(problem)
			}
			return result
		}
	}
	return e.failKnown(record, index, result, errors.New("rebase ended without an active operation or a verified resulting HEAD"))
}

func (e Executor) verifyRecovery(record *operationRecord, index int) error {
	planned, checkpoint := record.Plan.Repos[index], record.Repositories[index]
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	oid, exists, err := e.Git.RecoveryOID(ctx, planned.SourcePath, checkpoint.RecoveryRef)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("private recovery ref %s is missing", checkpoint.RecoveryRef)
	}
	if checkpoint.RecoveryOID != "" && oid != checkpoint.RecoveryOID {
		return fmt.Errorf("private recovery ref %s changed", checkpoint.RecoveryRef)
	}
	return nil
}

func (e Executor) deleteRecoveryIfPresent(record *operationRecord, index int) error {
	planned, checkpoint := record.Plan.Repos[index], record.Repositories[index]
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	oid, exists, err := e.Git.RecoveryOID(ctx, planned.SourcePath, checkpoint.RecoveryRef)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if oid != checkpoint.RecoveryOID {
		return fmt.Errorf("private recovery ref %s changed", checkpoint.RecoveryRef)
	}
	if err := e.Git.DeleteRecoveryRef(ctx, planned.SourcePath, checkpoint.RecoveryRef, checkpoint.RecoveryOID); err != nil {
		return err
	}
	return e.inject("recovery-ref-deleted:" + planned.ID)
}

func (e Executor) verifyRestored(planned RepositoryPlan, expectedHead, expectedFingerprint string) error {
	if expectedFingerprint == "" {
		return fmt.Errorf("expected working-tree fingerprint is unavailable")
	}
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkout.FullRef != planned.BranchRef || checkout.HeadOID != expectedHead {
		return fmt.Errorf("restored checkout identity or HEAD differs")
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if fingerprint != expectedFingerprint {
		return fmt.Errorf("restored working-tree fingerprint differs")
	}
	return nil
}

func (e Executor) appliedRecoveryFingerprint(planned RepositoryPlan, expectedHead string) (string, error) {
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return "", err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir || checkout.FullRef != planned.BranchRef || checkout.HeadOID != expectedHead {
		return "", fmt.Errorf("restored checkout identity or HEAD differs")
	}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return "", err
	}
	if len(operations) > 0 {
		return "", fmt.Errorf("active Git operation after recovery apply: %s", operations[0])
	}
	status, err := e.Git.WorkingTreeStatus(ctx, planned.Destination)
	if err != nil {
		return "", err
	}
	if status != planned.WorkingTree {
		return "", fmt.Errorf("restored working-tree status differs from pre-Sync state")
	}
	return e.Git.WorktreeFingerprint(ctx, planned.Destination)
}

func (e Executor) cleanFingerprintAtHead(planned RepositoryPlan, expectedHead string) (string, error) {
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return "", err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir || checkout.FullRef != planned.BranchRef || checkout.HeadOID != expectedHead {
		return "", fmt.Errorf("checkout identity, branch, or HEAD differs")
	}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return "", err
	}
	if len(operations) > 0 {
		return "", fmt.Errorf("active Git operation: %s", operations[0])
	}
	status, err := e.Git.WorkingTreeStatus(ctx, planned.Destination)
	if err != nil {
		return "", err
	}
	if workingTreeDirty(status) {
		return "", fmt.Errorf("working tree is dirty")
	}
	return e.Git.WorktreeFingerprint(ctx, planned.Destination)
}

func (e Executor) recordOwnedCurrentState(record *operationRecord, index int) error {
	planned := record.Plan.Repos[index]
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return fmt.Errorf("inspect operation-owned state: %w", err)
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir {
		return fmt.Errorf("operation-owned checkout identity differs")
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return fmt.Errorf("fingerprint operation-owned state: %w", err)
	}
	if fingerprint == "" {
		return fmt.Errorf("operation-owned working-tree fingerprint is empty")
	}
	record.Repositories[index].OwnedFingerprint = fingerprint
	if err := e.save(*record); err != nil {
		return fmt.Errorf("checkpoint operation-owned state: %w", err)
	}
	return nil
}

// checkpointImmediateRollbackState is only safe in the same uninterrupted call
// that just returned from a mutating Git command. Resume paths must instead use
// a fingerprint that was durably recorded before the process stopped.
func (e Executor) checkpointImmediateRollbackState(record *operationRecord, index int, cause error) error {
	planned := record.Plan.Repos[index]
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return fmt.Errorf("inspect rollback state: %w", err)
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir {
		return fmt.Errorf("rollback checkout identity differs")
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return fmt.Errorf("fingerprint rollback state: %w", err)
	}
	if fingerprint == "" {
		return fmt.Errorf("rollback working-tree fingerprint is empty")
	}
	checkpoint := &record.Repositories[index]
	checkpoint.Phase, checkpoint.Rollback, checkpoint.To = PhaseRestoreIntent, true, planned.PreHeadOID
	checkpoint.OwnedFingerprint, checkpoint.RestoredFingerprint = fingerprint, ""
	checkpoint.LastProblem = errorText(cause)
	if err := e.save(*record); err != nil {
		return fmt.Errorf("checkpoint rollback intent: %w", err)
	}
	return nil
}

func (e Executor) verifyOwnedState(planned RepositoryPlan, checkpoint checkpoint, expectedHead string) error {
	if checkpoint.OwnedFingerprint == "" {
		return fmt.Errorf("operation-owned working-tree fingerprint is unavailable")
	}
	ctx, cancel := e.reconciliationContext()
	defer cancel()
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir {
		return fmt.Errorf("checkout identity differs")
	}
	if expectedHead != "" && (checkout.FullRef != planned.BranchRef || checkout.HeadOID != expectedHead) {
		return fmt.Errorf("checkout branch or HEAD differs")
	}
	if expectedHead == "" && checkout.FullRef != planned.BranchRef {
		operations, operationErr := e.Git.ActiveOperations(ctx, planned.Destination)
		if operationErr != nil {
			return operationErr
		}
		activeRebase := false
		for _, operation := range operations {
			activeRebase = activeRebase || operation == gitops.OperationRebase
		}
		if !checkout.Detached || !activeRebase {
			return fmt.Errorf("checkout branch differs outside the recorded rebase")
		}
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if fingerprint != checkpoint.OwnedFingerprint {
		return fmt.Errorf("operation-owned working-tree state changed")
	}
	return nil
}

func (e Executor) verifyRetryablePreRebaseState(ctx context.Context, planned RepositoryPlan, checkpoint checkpoint) error {
	status, err := e.Git.WorkingTreeStatus(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if workingTreeDirty(status) {
		return fmt.Errorf("working tree is dirty")
	}
	if planned.LegacyFingerprintUnavailable {
		if workingTreeDirty(planned.WorkingTree) {
			return fmt.Errorf("legacy dirty operation has no exact fingerprint")
		}
		return nil
	}
	expected := planned.InitialFingerprint
	if workingTreeDirty(planned.WorkingTree) {
		expected = checkpoint.OwnedFingerprint
	}
	if expected == "" {
		return fmt.Errorf("expected working-tree fingerprint is unavailable")
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if fingerprint != expected {
		return fmt.Errorf("working-tree fingerprint changed")
	}
	return nil
}

func (e Executor) verifyRebaseStart(ctx context.Context, record *operationRecord, index int) error {
	planned, checkpoint := record.Plan.Repos[index], record.Repositories[index]
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir || checkout.FullRef != planned.BranchRef || checkout.HeadOID != planned.PreHeadOID {
		return fmt.Errorf("checkout identity, branch, or HEAD differs")
	}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if len(operations) > 0 {
		return fmt.Errorf("active Git operation: %s", operations[0])
	}
	if err := e.verifyRetryablePreRebaseState(ctx, planned, checkpoint); err != nil {
		return err
	}
	if workingTreeDirty(planned.WorkingTree) {
		return e.verifyRecovery(record, index)
	}
	return nil
}

func (e Executor) failKnown(record *operationRecord, index int, result RepositoryResult, err error) RepositoryResult {
	record.Repositories[index].Phase = PhaseFailedKnownState
	record.Repositories[index].LastProblem = errorText(err)
	if saveErr := e.save(*record); saveErr != nil {
		err = errors.Join(err, saveErr)
	}
	result.Status, result.Err = gitops.SyncFailed, err
	return result
}

func (e Executor) complete(record *operationRecord, index int, result RepositoryResult, status gitops.SyncStatus, to, problem string) RepositoryResult {
	checkpoint := &record.Repositories[index]
	checkpoint.Phase, checkpoint.ResultStatus = PhaseCompleted, status
	checkpoint.From, checkpoint.To, checkpoint.LastProblem = record.Plan.Repos[index].PreHeadOID, to, problem
	if err := e.save(*record); err != nil {
		result.Status, result.Err = gitops.SyncFailed, err
		return result
	}
	if err := e.inject("completed:" + record.Plan.Repos[index].ID); err != nil {
		result.Status, result.Err = gitops.SyncFailed, err
		return result
	}
	result.Status, result.To = status, to
	if problem != "" {
		result.Err = errors.New(problem)
	}
	return result
}

func (e Executor) loadOrStart(plan Plan) (operationRecord, error) {
	current, err := loadRecord(plan.OperationRecord)
	recordExists := err == nil
	if err == nil {
		current, migrated, err := migrateAndValidate(current, plan.WorkID)
		if err != nil {
			return operationRecord{}, err
		}
		if migrated {
			if err := work.ReplaceJSON(plan.OperationRecord, current, 0o600, nil); err != nil {
				return operationRecord{}, err
			}
		}
		if !terminal(current) {
			return current, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, err
	}
	operationID, err := e.operationID()
	if err != nil {
		return operationRecord{}, err
	}
	record := operationRecord{
		SchemaVersion: operationSchemaVersion, OperationID: operationID, Kind: "sync-work", Plan: plan,
		CreatedAt: e.now().UTC(), UpdatedAt: e.now().UTC(),
	}
	for _, repository := range plan.Repos {
		record.Repositories = append(record.Repositories, checkpoint{ID: repository.ID, Phase: PhasePending})
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		return record, err
	}
	if !recordExists {
		if err := work.CreateJSON(plan.OperationRecord, record, 0o600); err != nil {
			return record, err
		}
	} else if err := work.ReplaceJSON(plan.OperationRecord, record, 0o600, nil); err != nil {
		return record, err
	}
	return record, nil
}

func loadRecord(path string) (operationRecord, error) {
	var record operationRecord
	if err := work.LoadJSON(path, &record); err != nil {
		return operationRecord{}, err
	}
	if filepath.Clean(record.Plan.OperationRecord) != filepath.Clean(path) {
		return operationRecord{}, fmt.Errorf("Sync Work operation record path does not match its plan")
	}
	return record, nil
}

func (e Executor) save(record operationRecord) error {
	record.UpdatedAt = e.now().UTC()
	if err := validateRecord(record, record.Plan.WorkID); err != nil {
		return err
	}
	return work.ReplaceJSON(record.Plan.OperationRecord, record, 0o600, nil)
}

func (e Executor) operationID() (string, error) {
	if e.OperationID != nil {
		return e.OperationID()
	}
	return randomOperationID()
}

func randomOperationID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate Sync operation ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Executor) reconciliationContext() (context.Context, context.CancelFunc) {
	duration := e.ReconcileFor
	if duration <= 0 {
		duration = 30 * time.Second
	}
	return context.WithTimeout(context.Background(), duration)
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
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if planned.InitialFingerprint == "" || fingerprint != planned.InitialFingerprint {
		return fmt.Errorf("working-tree fingerprint changed")
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

func (e Executor) revalidatePreservationIntent(ctx context.Context, planned RepositoryPlan) error {
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir || checkout.FullRef != planned.BranchRef || checkout.HeadOID != planned.PreHeadOID {
		return fmt.Errorf("checkout identity, branch, or HEAD differs from preservation intent")
	}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if len(operations) > 0 {
		return fmt.Errorf("active Git operation: %s", operations[0])
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if fingerprint == planned.InitialFingerprint && fingerprint != "" {
		return nil
	}
	return fmt.Errorf("preservation may have completed before its exact result was durably recorded; recovery state is retained")
}

func (e Executor) revalidatePreserved(ctx context.Context, planned RepositoryPlan, checkpoint checkpoint) error {
	checkout, err := e.Git.InspectCheckout(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != planned.GitCommonDir || checkout.FullRef != planned.BranchRef || checkout.HeadOID != planned.PreHeadOID {
		return fmt.Errorf("checkout identity, branch, or HEAD differs after preservation")
	}
	operations, err := e.Git.ActiveOperations(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if len(operations) > 0 {
		return fmt.Errorf("active Git operation: %s", operations[0])
	}
	status, err := e.Git.WorkingTreeStatus(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if workingTreeDirty(status) {
		return fmt.Errorf("working tree is dirty after preservation")
	}
	fingerprint, err := e.Git.WorktreeFingerprint(ctx, planned.Destination)
	if err != nil {
		return err
	}
	if checkpoint.OwnedFingerprint == "" || fingerprint != checkpoint.OwnedFingerprint {
		return fmt.Errorf("preserved working-tree fingerprint changed")
	}
	return nil
}

func (e Executor) inject(point string) error {
	if e.Fault == nil {
		return nil
	}
	if err := e.Fault(point); err != nil {
		return injectedFault{point: point, err: err}
	}
	return nil
}

type injectedFault struct {
	point string
	err   error
}

func (e injectedFault) Error() string { return fmt.Sprintf("injected fault at %s: %v", e.point, e.err) }
func (e injectedFault) Unwrap() error { return e.err }

func isInjectedFault(err error) bool {
	var target injectedFault
	return errors.As(err, &target)
}

func syncMutationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.WithCancel(context.Background())
}

func workingTreeDirty(status gitops.WorkingTreeStatus) bool {
	return status.Staged > 0 || status.Unstaged > 0 || status.Untracked > 0 || status.Conflicted > 0
}

func recoveryRef(workID, operationID, repositoryID string) string {
	return "refs/goworktree/recovery/" + workID + "/" + operationID + "/" + repositoryHash(repositoryID)
}

func recoveryMarker(workID, operationID, repositoryID string) string {
	return "goworktree-sync:" + workID + ":" + operationID + ":" + repositoryHash(repositoryID)
}

func repositoryHash(repositoryID string) string {
	digest := sha256.Sum256([]byte(repositoryID))
	return hex.EncodeToString(digest[:8])
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
