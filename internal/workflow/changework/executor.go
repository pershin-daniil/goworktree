package changework

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
)

const OperationSchemaVersion = 1

type Phase string

const (
	PhaseRecorded     Phase = "recorded"
	PhaseRepositories Phase = "repositories"
	PhaseHarness      Phase = "harness"
	PhaseManifest     Phase = "manifest"
	PhaseInterrupted  Phase = "interrupted"
	PhaseCompleted    Phase = "completed"
)

type StepState string

const (
	StepPending StepState = "pending"
	StepIntent  StepState = "intent-recorded"
	StepDone    StepState = "done"
)

type OperationRecord struct {
	SchemaVersion   int                    `json:"schema_version"`
	OperationID     string                 `json:"operation_id"`
	WorkID          work.Identity          `json:"work_id"`
	Kind            Kind                   `json:"kind"`
	Phase           Phase                  `json:"phase"`
	Plan            Plan                   `json:"plan"`
	Repositories    []RepositoryCheckpoint `json:"repositories"`
	HarnessUpdated  bool                   `json:"harness_updated"`
	ManifestUpdated bool                   `json:"manifest_updated"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
	LastProblem     string                 `json:"last_problem,omitempty"`
}

type RepositoryCheckpoint struct {
	ID              string    `json:"id"`
	State           StepState `json:"state"`
	WorktreeRemoved bool      `json:"worktree_removed,omitempty"`
	BranchDeleted   bool      `json:"branch_deleted,omitempty"`
	Error           string    `json:"error,omitempty"`
}

func (r OperationRecord) Validate() error {
	if r.SchemaVersion != OperationSchemaVersion || r.OperationID == "" || r.OperationID != r.Plan.OperationID {
		return fmt.Errorf("invalid Change Work operation identity")
	}
	if r.WorkID == "" || r.WorkID != r.Plan.WorkID || r.Kind != r.Plan.Kind {
		return fmt.Errorf("Change Work record does not match plan")
	}
	if err := r.Plan.Validate(); err != nil {
		return err
	}
	if len(r.Repositories) != len(r.Plan.Repositories) {
		return fmt.Errorf("Change Work repository checkpoints do not match plan")
	}
	for index, checkpoint := range r.Repositories {
		if checkpoint.ID != r.Plan.Repositories[index].ID {
			return fmt.Errorf("Change Work checkpoint %d does not match plan", index)
		}
		switch checkpoint.State {
		case StepPending, StepIntent, StepDone:
		default:
			return fmt.Errorf("repository %s has invalid checkpoint %q", checkpoint.ID, checkpoint.State)
		}
		if r.Phase == PhaseCompleted {
			planned := r.Plan.Repositories[index]
			if checkpoint.State != StepDone || checkpoint.Error != "" {
				return fmt.Errorf("completed Change Work repository %s is not complete", checkpoint.ID)
			}
			if r.Kind == KindRemove && !checkpoint.WorktreeRemoved {
				return fmt.Errorf("completed Change Work repository %s still has a worktree", checkpoint.ID)
			}
			if planned.DeleteBranch && !checkpoint.BranchDeleted {
				return fmt.Errorf("completed Change Work repository %s still has its planned branch", checkpoint.ID)
			}
		}
	}
	switch r.Phase {
	case PhaseRecorded, PhaseRepositories, PhaseHarness, PhaseManifest, PhaseInterrupted, PhaseCompleted:
	default:
		return fmt.Errorf("invalid Change Work phase %q", r.Phase)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		return fmt.Errorf("Change Work timestamps are incomplete")
	}
	if r.Phase == PhaseCompleted && (!r.HarnessUpdated || !r.ManifestUpdated || r.LastProblem != "") {
		return fmt.Errorf("completed Change Work has incomplete final checkpoints")
	}
	return nil
}

func LoadRecord(path string) (OperationRecord, error) {
	var record OperationRecord
	if err := work.LoadJSON(path, &record); err != nil {
		return OperationRecord{}, err
	}
	if err := record.Validate(); err != nil {
		return OperationRecord{}, fmt.Errorf("validate Change Work operation: %w", err)
	}
	if filepath.Clean(record.Plan.OperationPath) != filepath.Clean(path) {
		return OperationRecord{}, fmt.Errorf("Change Work operation path mismatch")
	}
	return record, nil
}

// RecordMatchesManifest reports whether a completed operation owns the exact
// current manifest revision, not only its revision number and operation ID.
func RecordMatchesManifest(record OperationRecord, manifest work.Manifest) bool {
	return record.Phase == PhaseCompleted && record.OperationID == manifest.LastChangeID && sameJSON(record.Plan.After, manifest)
}

type ExecutionGit interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	CreateWorktreeAtOID(context.Context, string, string, string, string) error
	AttachWorktree(context.Context, string, string, string) error
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	WorktreeFingerprint(context.Context, string) (string, error)
	RemoveWorktree(context.Context, string, string) error
	DeleteLocalBranchAtOID(context.Context, string, string, string) error
}

func (SystemGit) CreateWorktreeAtOID(ctx context.Context, repo, destination, branchRef, oid string) error {
	return gitops.CreateWorktreeAtOIDContext(ctx, repo, destination, branchRef, oid)
}
func (SystemGit) AttachWorktree(ctx context.Context, repo, destination, branchRef string) error {
	return gitops.AttachWorktreeContext(ctx, repo, destination, branchRef)
}
func (SystemGit) RemoveWorktree(ctx context.Context, source, destination string) error {
	return gitops.RemoveCleanWorktreeContext(ctx, source, destination)
}
func (SystemGit) DeleteLocalBranchAtOID(ctx context.Context, source, ref, oid string) error {
	return gitops.DeleteLocalBranchAtOIDContext(ctx, source, ref, oid)
}

type ExecutionLocker interface {
	AcquireExecution(context.Context, string, []string) (func() error, error)
}

type FileLocker struct{ Set lockops.Set }

func (l FileLocker) AcquireExecution(ctx context.Context, workID string, repositories []string) (func() error, error) {
	workLease, err := l.Set.Acquire(ctx, "works", []string{workID})
	if err != nil {
		return nil, err
	}
	repositoryLease, err := l.Set.Acquire(ctx, "repositories", repositories)
	if err != nil {
		return nil, errors.Join(err, workLease.Release())
	}
	return func() error { return errors.Join(repositoryLease.Release(), workLease.Release()) }, nil
}

type Executor struct {
	Git    ExecutionGit
	Locker ExecutionLocker
	Now    func() time.Time
}

func (e Executor) Execute(ctx context.Context, plan Plan) (Result, error) {
	if err := plan.Validate(); err != nil {
		return Result{}, err
	}
	return e.withLocks(ctx, plan, func() (Result, error) {
		current, err := loadManifest(plan.WorkRoot)
		if err != nil {
			return Result{}, err
		}
		if !sameJSON(current, plan.Before) {
			return Result{}, fmt.Errorf("Work manifest changed after planning")
		}
		record := newRecord(plan, e.now())
		if err := createOrReplaceRecord(record); err != nil {
			return Result{}, err
		}
		return e.continueRecord(ctx, &record)
	})
}

func (e Executor) Resume(ctx context.Context, path string) (Result, error) {
	return e.resume(ctx, path, nil)
}

// ResumeMatching checks the request again under the operation's locks so a
// completed/replaced record cannot redirect a CLI retry to another operation.
func (e Executor) ResumeMatching(ctx context.Context, path string, request ResumeRequest) (Result, error) {
	return e.resume(ctx, path, &request)
}

func (e Executor) resume(ctx context.Context, path string, request *ResumeRequest) (Result, error) {
	record, err := LoadRecord(path)
	if err != nil {
		return Result{}, err
	}
	return e.withLocks(ctx, record.Plan, func() (Result, error) {
		locked, err := LoadRecord(path)
		if err != nil {
			return Result{}, err
		}
		if locked.OperationID != record.OperationID {
			return Result{}, fmt.Errorf("Change Work operation changed while acquiring locks")
		}
		if request != nil {
			if err := locked.Plan.MatchRequest(*request); err != nil {
				return Result{}, err
			}
		}
		return e.continueRecord(ctx, &locked)
	})
}

func (e Executor) continueRecord(ctx context.Context, record *OperationRecord) (Result, error) {
	manifest, err := loadManifest(record.Plan.WorkRoot)
	if err != nil {
		cause := fmt.Errorf("load Work manifest before continuing: %w", err)
		if record.Phase != PhaseCompleted {
			return e.interrupt(record, cause)
		}
		return resultFromRecord(*record), cause
	}
	manifestIsBefore := sameJSON(manifest, record.Plan.Before)
	manifestIsAfter := sameJSON(manifest, record.Plan.After)
	if !manifestIsBefore && !manifestIsAfter {
		cause := fmt.Errorf("Work manifest changed after the repository change was confirmed")
		if record.Phase != PhaseCompleted {
			return e.interrupt(record, cause)
		}
		return resultFromRecord(*record), cause
	}
	if err := verifyContinuationHarness(record.Plan); err != nil {
		if record.Phase != PhaseCompleted {
			return e.interrupt(record, err)
		}
		return resultFromRecord(*record), err
	}
	if record.Phase == PhaseCompleted {
		if !manifestIsAfter {
			return resultFromRecord(*record), fmt.Errorf("completed repository change does not own the current Work manifest")
		}
		return resultFromRecord(*record), nil
	}
	record.Phase = PhaseRepositories
	record.LastProblem = ""
	if err := saveRecord(*record, e.now()); err != nil {
		return Result{}, err
	}
	for index := range record.Plan.Repositories {
		if err := ctx.Err(); err != nil {
			return e.interrupt(record, err)
		}
		var err error
		switch record.Plan.Kind {
		case KindAdd:
			err = e.ensureAdded(ctx, record, index)
		case KindRemove:
			err = e.ensureRemoved(ctx, record, index)
		case KindAdopt:
			err = e.ensureAdopted(ctx, record, index)
		}
		if err != nil {
			record.Repositories[index].Error = err.Error()
			return e.interrupt(record, err)
		}
		if record.Repositories[index].Error != "" {
			record.Repositories[index].Error = ""
			if err := saveRecord(*record, e.now()); err != nil {
				return Result{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return e.interrupt(record, err)
	}

	record.Phase = PhaseHarness
	if err := saveRecord(*record, e.now()); err != nil {
		return Result{}, err
	}
	if !record.HarnessUpdated {
		if err := syncHarness(record.Plan); err != nil {
			return e.interrupt(record, fmt.Errorf("update go.work: %w", err))
		}
		record.HarnessUpdated = true
		if err := saveRecord(*record, e.now()); err != nil {
			return Result{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return e.interrupt(record, err)
	}

	record.Phase = PhaseManifest
	if err := saveRecord(*record, e.now()); err != nil {
		return Result{}, err
	}
	if !record.ManifestUpdated {
		if err := publishManifest(record.Plan); err != nil {
			return e.interrupt(record, fmt.Errorf("publish Work manifest: %w", err))
		}
		record.ManifestUpdated = true
		if err := saveRecord(*record, e.now()); err != nil {
			return Result{}, err
		}
	}
	record.Phase = PhaseCompleted
	record.LastProblem = ""
	if err := saveRecord(*record, e.now()); err != nil {
		return Result{}, err
	}
	return resultFromRecord(*record), nil
}

func (e Executor) ensureAdded(ctx context.Context, record *OperationRecord, index int) error {
	checkpoint := &record.Repositories[index]
	plan := record.Plan.Repositories[index]
	if checkpoint.State == StepDone {
		return e.verifyAdded(ctx, plan)
	}
	if err := e.verifyAdded(ctx, plan); err == nil {
		checkpoint.State = StepDone
		return saveRecord(*record, e.now())
	}
	identity, err := e.Git.InspectRepository(ctx, plan.SourcePath)
	if err != nil || identity.CommonDir != plan.GitCommonDir {
		return fmt.Errorf("repository %s source identity changed", plan.ID)
	}
	branchOID, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef)
	if err != nil {
		return err
	}
	if plan.Reattach {
		if !exists || branchOID != plan.BranchOID {
			return fmt.Errorf("repository %s retained branch changed after planning", plan.ID)
		}
	} else if exists {
		return fmt.Errorf("repository %s target branch appeared after planning", plan.ID)
	}
	if _, err := os.Lstat(plan.Destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("repository %s destination appeared after planning", plan.ID)
		}
		return err
	}
	checkpoint.State = StepIntent
	if err := saveRecord(*record, e.now()); err != nil {
		return err
	}
	if plan.Reattach {
		err = e.Git.AttachWorktree(ctx, plan.SourcePath, plan.Destination, plan.BranchRef)
	} else {
		err = e.Git.CreateWorktreeAtOID(ctx, plan.SourcePath, plan.Destination, plan.BranchRef, plan.BaseOID)
	}
	if err != nil {
		if verifyErr := e.verifyAdded(ctx, plan); verifyErr != nil {
			return errors.Join(err, verifyErr)
		}
	}
	if err := e.verifyAdded(ctx, plan); err != nil {
		return err
	}
	checkpoint.State = StepDone
	return saveRecord(*record, e.now())
}

func (e Executor) verifyAdded(ctx context.Context, plan RepositoryPlan) error {
	if err := e.verifySourceIdentity(ctx, plan); err != nil {
		return err
	}
	checkout, err := e.Git.InspectCheckout(ctx, plan.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != plan.GitCommonDir || checkout.FullRef != plan.BranchRef || checkout.Detached {
		return fmt.Errorf("repository %s checkout does not match planned identity", plan.ID)
	}
	branchOID, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef)
	if err != nil || !exists || branchOID != checkout.HeadOID || branchOID != plan.BranchOID {
		return fmt.Errorf("repository %s branch does not match planned OID", plan.ID)
	}
	registrations, err := e.Git.ListWorktrees(ctx, plan.SourcePath)
	if err != nil {
		return fmt.Errorf("repository %s: inspect worktree registrations: %w", plan.ID, err)
	}
	destinationMatches, branchMatches, exactMatches := 0, 0, 0
	for _, registration := range registrations {
		destinationMatch := samePath(registration.Path, plan.Destination)
		branchMatch := registration.Branch == plan.BranchRef
		if destinationMatch {
			destinationMatches++
		}
		if branchMatch {
			branchMatches++
		}
		if destinationMatch && branchMatch && !registration.Detached && registration.HeadOID == plan.BranchOID {
			exactMatches++
		}
	}
	if destinationMatches != 1 || branchMatches != 1 || exactMatches != 1 {
		return fmt.Errorf("repository %s worktree registration does not match the planned destination and branch (destination=%d branch=%d exact=%d registrations=%+v)",
			plan.ID, destinationMatches, branchMatches, exactMatches, registrations)
	}
	return nil
}

func (e Executor) ensureRemoved(ctx context.Context, record *OperationRecord, index int) error {
	checkpoint := &record.Repositories[index]
	plan := record.Plan.Repositories[index]
	if err := e.verifySourceIdentity(ctx, plan); err != nil {
		return err
	}
	if checkpoint.WorktreeRemoved {
		if _, err := os.Lstat(plan.Destination); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("repository %s destination reappeared after removal", plan.ID)
			}
			return err
		}
		if err := e.ensureWorktreeUnregistered(ctx, plan); err != nil {
			return err
		}
	}
	if !checkpoint.WorktreeRemoved {
		if _, err := os.Lstat(plan.Destination); errors.Is(err, os.ErrNotExist) {
			if err := e.ensureWorktreeUnregistered(ctx, plan); err != nil {
				return err
			}
			checkpoint.WorktreeRemoved = true
		} else if err != nil {
			return err
		} else {
			status, err := e.Git.WorkingTreeStatus(ctx, plan.Destination)
			if err != nil || status.Dirty() {
				return fmt.Errorf("repository %s is no longer clean", plan.ID)
			}
			operations, err := e.Git.ActiveOperations(ctx, plan.Destination)
			if err != nil || len(operations) != 0 {
				return fmt.Errorf("repository %s has an active or unknown Git operation", plan.ID)
			}
			fingerprint, err := e.Git.WorktreeFingerprint(ctx, plan.Destination)
			if err != nil || fingerprint != plan.Fingerprint {
				return fmt.Errorf("repository %s changed after planning", plan.ID)
			}
			checkpoint.State = StepIntent
			if err := saveRecord(*record, e.now()); err != nil {
				return err
			}
			if err := e.Git.RemoveWorktree(ctx, plan.SourcePath, plan.Destination); err != nil {
				if _, statErr := os.Lstat(plan.Destination); !errors.Is(statErr, os.ErrNotExist) {
					return err
				}
			}
			if err := e.ensureWorktreeUnregistered(ctx, plan); err != nil {
				return err
			}
			checkpoint.WorktreeRemoved = true
		}
		if err := saveRecord(*record, e.now()); err != nil {
			return err
		}
	}
	if plan.DeleteBranch && !checkpoint.BranchDeleted {
		oid, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef)
		if err != nil {
			return err
		}
		if exists {
			if oid != plan.BranchOID {
				return fmt.Errorf("repository %s branch changed after planning", plan.ID)
			}
			if err := e.Git.DeleteLocalBranchAtOID(ctx, plan.SourcePath, plan.BranchRef, plan.BranchOID); err != nil {
				return err
			}
		}
		checkpoint.BranchDeleted = true
		if err := saveRecord(*record, e.now()); err != nil {
			return err
		}
	}
	if plan.DeleteBranch && checkpoint.BranchDeleted {
		if _, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef); err != nil {
			return err
		} else if exists {
			return fmt.Errorf("repository %s deleted branch reappeared", plan.ID)
		}
	}
	if !plan.DeleteBranch {
		oid, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef)
		if err != nil {
			return err
		}
		if !exists || oid != plan.BranchOID {
			return fmt.Errorf("repository %s retained branch changed after planning", plan.ID)
		}
	}
	checkpoint.State = StepDone
	return saveRecord(*record, e.now())
}

func (e Executor) verifySourceIdentity(ctx context.Context, plan RepositoryPlan) error {
	identity, err := e.Git.InspectRepository(ctx, plan.SourcePath)
	if err != nil {
		return fmt.Errorf("repository %s: inspect source identity: %w", plan.ID, err)
	}
	if identity.CommonDir != plan.GitCommonDir {
		return fmt.Errorf("repository %s source identity changed after planning", plan.ID)
	}
	return nil
}

func (e Executor) ensureWorktreeUnregistered(ctx context.Context, plan RepositoryPlan) error {
	registrations, err := e.Git.ListWorktrees(ctx, plan.SourcePath)
	if err != nil {
		return fmt.Errorf("repository %s: inspect worktree registrations: %w", plan.ID, err)
	}
	if !hasPlannedRegistration(registrations, plan) {
		return nil
	}
	removeErr := e.Git.RemoveWorktree(ctx, plan.SourcePath, plan.Destination)
	registrations, inspectErr := e.Git.ListWorktrees(ctx, plan.SourcePath)
	if inspectErr != nil {
		return errors.Join(removeErr, fmt.Errorf("repository %s: verify worktree removal: %w", plan.ID, inspectErr))
	}
	if hasPlannedRegistration(registrations, plan) {
		return errors.Join(removeErr, fmt.Errorf("repository %s worktree or branch remains registered after removal", plan.ID))
	}
	return nil
}

func hasPlannedRegistration(registrations []gitops.WorktreeRegistration, plan RepositoryPlan) bool {
	for _, registration := range registrations {
		if samePath(registration.Path, plan.Destination) || registration.Branch == plan.BranchRef {
			return true
		}
	}
	return false
}

func (e Executor) interrupt(record *OperationRecord, cause error) (Result, error) {
	record.Phase = PhaseInterrupted
	record.LastProblem = cause.Error()
	return resultFromRecord(*record), errors.Join(cause, saveRecord(*record, e.now()))
}

func (e Executor) withLocks(ctx context.Context, plan Plan, run func() (Result, error)) (Result, error) {
	if e.Git == nil || e.Locker == nil {
		return Result{}, fmt.Errorf("Change Work executor dependencies are incomplete")
	}
	identities := make([]string, 0, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		identities = append(identities, repository.GitCommonDir)
	}
	sort.Strings(identities)
	release, err := e.Locker.AcquireExecution(ctx, plan.WorkID.String(), identities)
	if err != nil {
		return Result{}, err
	}
	result, runErr := run()
	return result, errors.Join(runErr, release())
}

func newRecord(plan Plan, now time.Time) OperationRecord {
	checkpoints := make([]RepositoryCheckpoint, 0, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		checkpoints = append(checkpoints, RepositoryCheckpoint{ID: repository.ID, State: StepPending})
	}
	return OperationRecord{
		SchemaVersion: OperationSchemaVersion, OperationID: plan.OperationID, WorkID: plan.WorkID,
		Kind: plan.Kind, Phase: PhaseRecorded, Plan: plan, Repositories: checkpoints,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
}

func createOrReplaceRecord(record OperationRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(record.Plan.OperationPath)
	if err := ensureChangeOperationDirectory(dir); err != nil {
		return err
	}
	if err := work.CreateJSON(record.Plan.OperationPath, record, 0o600); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return work.ReplaceJSON(record.Plan.OperationPath, record, 0o600, func(current []byte) error {
		var previous OperationRecord
		if err := json.Unmarshal(current, &previous); err != nil {
			return err
		}
		if err := previous.Validate(); err != nil {
			return err
		}
		if previous.Phase != PhaseCompleted {
			return fmt.Errorf("unfinished Change Work operation exists")
		}
		if !RecordMatchesManifest(previous, record.Plan.Before) {
			return fmt.Errorf("previous Change Work operation does not own the current manifest")
		}
		return nil
	})
}

func ensureChangeOperationDirectory(path string) error {
	changeWorkDir := filepath.Clean(path)
	operationsDir := filepath.Dir(changeWorkDir)
	controlRoot := filepath.Dir(operationsDir)
	if filepath.Base(changeWorkDir) != "change-work" || filepath.Base(operationsDir) != "operations" {
		return fmt.Errorf("Change Work operation path has invalid layout")
	}
	for _, dir := range []string{controlRoot, operationsDir, changeWorkDir} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			if dir == controlRoot {
				return fmt.Errorf("control root does not exist: %s", controlRoot)
			}
			if err := os.Mkdir(dir, 0o700); err != nil {
				if !errors.Is(err, os.ErrExist) {
					return err
				}
				info, err = os.Lstat(dir)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("concurrently created Change Work operation path is unsafe: %s", dir)
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Change Work operation path component is not an owned directory: %s", dir)
		}
	}
	return nil
}

func saveRecord(record OperationRecord, now time.Time) error {
	record.UpdatedAt = now.UTC()
	if err := record.Validate(); err != nil {
		return err
	}
	return work.ReplaceJSON(record.Plan.OperationPath, record, 0o600, func(current []byte) error {
		var stored OperationRecord
		if err := json.Unmarshal(current, &stored); err != nil {
			return err
		}
		if stored.OperationID != record.OperationID || stored.WorkID != record.WorkID || !sameJSON(stored.Plan, record.Plan) {
			return fmt.Errorf("Change Work operation ownership or immutable plan changed")
		}
		return nil
	})
}

func syncHarness(plan Plan) error {
	after, afterExists, err := RenderManifestHarness(plan.After, plan.WorkRoot)
	if err != nil {
		return err
	}
	path := filepath.Join(plan.WorkRoot, "go.work")
	current, readErr := work.ReadRegularFile(path)
	currentExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if currentExists == afterExists && bytes.Equal(current, after) {
		return nil
	}
	if currentExists != plan.HarnessBeforeExists || !bytes.Equal(current, plan.HarnessBefore) {
		return fmt.Errorf("go.work changed after planning")
	}
	switch {
	case !currentExists && afterExists:
		return work.CreateFile(path, after, 0o644)
	case currentExists && afterExists:
		return work.ReplaceFile(path, after, 0o644, func(value []byte) error {
			if !bytes.Equal(value, plan.HarnessBefore) {
				return fmt.Errorf("go.work changed after validation")
			}
			return nil
		})
	case currentExists && !afterExists:
		if err := os.Remove(path); err != nil {
			return err
		}
		return work.SyncDirectory(plan.WorkRoot)
	default:
		return nil
	}
}

func verifyContinuationHarness(plan Plan) error {
	path := filepath.Join(plan.WorkRoot, "go.work")
	current, readErr := work.ReadRegularFile(path)
	currentExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read go.work before continuing: %w", readErr)
	}
	if currentExists == plan.HarnessBeforeExists && bytes.Equal(current, plan.HarnessBefore) {
		return nil
	}
	after, afterExists, err := RenderManifestHarness(plan.After, plan.WorkRoot)
	if err == nil && currentExists == afterExists && bytes.Equal(current, after) {
		return nil
	}
	return fmt.Errorf("go.work changed after the repository change was confirmed")
}

func publishManifest(plan Plan) error {
	current, err := loadManifest(plan.WorkRoot)
	if err != nil {
		return err
	}
	if sameJSON(current, plan.After) {
		return nil
	}
	if !sameJSON(current, plan.Before) {
		return fmt.Errorf("Work manifest changed after planning")
	}
	path := filepath.Join(plan.WorkRoot, ".goworktree.json")
	return work.ReplaceJSON(path, plan.After, 0o644, func(value []byte) error {
		var stored work.Manifest
		if err := json.Unmarshal(value, &stored); err != nil {
			return err
		}
		if !sameJSON(stored, plan.Before) {
			return fmt.Errorf("Work manifest changed after validation")
		}
		return nil
	})
}

func loadManifest(root string) (work.Manifest, error) {
	var manifest work.Manifest
	if err := work.LoadJSON(filepath.Join(root, ".goworktree.json"), &manifest); err != nil {
		return work.Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return work.Manifest{}, err
	}
	return manifest, nil
}

func resultFromRecord(record OperationRecord) Result {
	result := Result{
		WorkName: record.Plan.WorkName.String(), Kind: record.Kind,
		Revision: record.Plan.After.Revision, OperationPath: record.Plan.OperationPath,
	}
	for _, checkpoint := range record.Repositories {
		status := "pending"
		if checkpoint.State == StepDone {
			status = "completed"
		}
		var repositoryErr error
		if checkpoint.Error != "" {
			repositoryErr = errors.New(checkpoint.Error)
		}
		result.Repositories = append(result.Repositories, RepositoryResult{ID: checkpoint.ID, Status: status, Err: repositoryErr})
	}
	return result
}

func sameJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
