package newwork

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
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

type ExecutionStatus string

const (
	ExecutionCreated     ExecutionStatus = "created"
	ExecutionPartial     ExecutionStatus = "partial"
	ExecutionInterrupted ExecutionStatus = "interrupted"
)

type ExecutionResult struct {
	Status               ExecutionStatus
	WorkRoot             string
	OperationRecordPath  string
	VerifiedRepositories []string
}

type RecordStore interface {
	Create(OperationRecord) error
	Load(string) (OperationRecord, error)
	Save(OperationRecord) error
}

type Executor struct {
	Git     ExecutionGit
	Locker  ExecutionLocker
	Store   RecordStore
	Now     func() time.Time
	NewID   func() (string, error)
	Harness HarnessManager
}

func (e Executor) Execute(ctx context.Context, plan Plan) (ExecutionResult, error) {
	if err := validatePlanShape(plan); err != nil {
		return ExecutionResult{}, problems(Problem{Code: CodeInvalidInput, Operation: "validate-execution-plan", Cause: err})
	}
	if problem := e.validateDependencies(); problem != nil {
		return ExecutionResult{}, problems(*problem)
	}
	return e.withLocks(ctx, plan, func() (ExecutionResult, error) {
		if err := e.revalidateInitialPlan(ctx, plan); err != nil {
			return ExecutionResult{}, err
		}
		operationID, err := e.newID()
		if err != nil {
			return ExecutionResult{}, problems(Problem{Code: CodeInternal, Operation: "generate-operation-id", Cause: err})
		}
		record := newOperationRecord(plan, operationID, e.now())
		if err := e.Store.Create(record); err != nil {
			code := CodeInternal
			if errors.Is(err, os.ErrExist) {
				code = CodeAlreadyExists
			}
			return ExecutionResult{}, problems(Problem{Code: code, Operation: "record-new-work-intent", Path: plan.OperationRecordPath, Cause: err})
		}
		return e.continueOperation(ctx, &record, true)
	})
}

func (e Executor) Resume(ctx context.Context, operationRecordPath string) (ExecutionResult, error) {
	if problem := e.validateDependencies(); problem != nil {
		return ExecutionResult{}, problems(*problem)
	}
	record, err := e.Store.Load(operationRecordPath)
	if err != nil {
		return ExecutionResult{}, problems(Problem{Code: CodeInvalidState, Operation: "load-new-work-operation", Path: operationRecordPath, Cause: err})
	}
	if err := validatePlanShape(record.Plan); err != nil {
		return ExecutionResult{}, problems(Problem{Code: CodeInvalidState, Operation: "validate-recorded-plan", Path: operationRecordPath, Cause: err})
	}
	return e.withLocks(ctx, record.Plan, func() (ExecutionResult, error) {
		lockedRecord, err := e.Store.Load(operationRecordPath)
		if err != nil {
			return ExecutionResult{}, problems(Problem{Code: CodeInvalidState, Operation: "reload-new-work-operation", Path: operationRecordPath, Cause: err})
		}
		if lockedRecord.OperationID != record.OperationID || lockedRecord.WorkID != record.WorkID {
			return ExecutionResult{}, problems(Problem{Code: CodeStateConflict, Operation: "revalidate-new-work-operation", Path: operationRecordPath, Cause: fmt.Errorf("operation identity changed while acquiring locks")})
		}
		return e.continueOperation(ctx, &lockedRecord, false)
	})
}

func (e Executor) continueOperation(ctx context.Context, record *OperationRecord, fresh bool) (ExecutionResult, error) {
	if err := work.CleanupAtomicTemps(record.Plan.OperationRecordPath); err != nil {
		return e.fail(record, problemFromError(CodeInvalidState, "clean-operation-temporary-files", "", record.Plan.OperationRecordPath, err))
	}
	if record.Phase == PhaseCreated {
		if err := e.verifyCreatedWork(ctx, *record); err != nil {
			return e.fail(record, problemFromError(CodeStateConflict, "inspect-created-work", "", record.Plan.WorkRoot, err))
		}
		return resultFromRecord(*record, ExecutionCreated), nil
	}
	if err := ctx.Err(); err != nil {
		return e.fail(record, problemFromError(codeFor(err, CodeInterrupted), "cancel-before-work-root", "", record.Plan.WorkRoot, err))
	}
	if err := e.ensureWorkRoot(record, fresh); err != nil {
		return e.fail(record, problemFromError(CodeStateConflict, "prepare-work-root", "", record.Plan.WorkRoot, err))
	}
	if err := e.continueRepositories(ctx, record); err != nil {
		return resultFromRecord(*record, statusForError(err)), err
	}
	if err := ctx.Err(); err != nil {
		return e.fail(record, problemFromError(codeFor(err, CodeInterrupted), "cancel-before-harness", "", record.Plan.WorkRoot, err))
	}
	if err := e.continueHarness(record); err != nil {
		return e.fail(record, problemFromError(CodeStateConflict, "generate-go-work", "", filepath.Join(record.Plan.WorkRoot, "go.work"), err))
	}

	record.Phase = PhaseVerifying
	record.UpdatedAt = e.now().UTC()
	if err := e.Store.Save(*record); err != nil {
		return e.fail(record, problemFromError(CodeInternal, "checkpoint-final-verification", "", record.Plan.OperationRecordPath, err))
	}
	if err := e.verifyCompleted(ctx, *record, false); err != nil {
		return e.fail(record, problemFromError(CodeStateConflict, "verify-new-work", "", record.Plan.WorkRoot, err))
	}
	record.Phase = PhaseCreated
	record.LastProblem = nil
	record.UpdatedAt = e.now().UTC()
	if err := e.Store.Save(*record); err != nil {
		return ExecutionResult{}, problems(Problem{Code: CodeInternal, Operation: "checkpoint-created-work", Path: record.Plan.OperationRecordPath, Cause: err})
	}
	return resultFromRecord(*record, ExecutionCreated), nil
}

func (e Executor) verifyCreatedWork(ctx context.Context, record OperationRecord) error {
	if err := verifyManifest(record.Plan.ManifestPath, manifestFromOperation(record)); err != nil {
		return err
	}
	for i, plan := range record.Plan.Repositories {
		if record.Repositories[i].State != StepVerified {
			return fmt.Errorf("repository %q lacks a verified creation checkpoint", plan.ID)
		}
		facts, err := e.inspectRepositoryStep(ctx, plan)
		if err != nil {
			return fmt.Errorf("repository %q: %w", plan.ID, err)
		}
		if err := facts.verify(plan, "", true); err != nil {
			return fmt.Errorf("repository %q: %w", plan.ID, err)
		}
	}
	return nil
}

func (e Executor) ensureWorkRoot(record *OperationRecord, fresh bool) error {
	expected := manifestFromOperation(*record)
	if err := expected.Validate(); err != nil {
		return fmt.Errorf("build manifest intent: %w", err)
	}
	if !record.WorkRootReady {
		record.Phase = PhaseCreatingWorkRoot
		record.UpdatedAt = e.now().UTC()
		if err := e.Store.Save(*record); err != nil {
			return fmt.Errorf("checkpoint Work root intent: %w", err)
		}
	}

	info, err := os.Lstat(record.Plan.WorkRoot)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(record.Plan.WorkRoot, 0o755); err != nil {
			return fmt.Errorf("create Work root: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect Work root: %w", err)
	} else if fresh {
		return fmt.Errorf("Work root appeared after plan validation")
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Work root is not an owned directory")
	}
	if err := work.CleanupAtomicTemps(record.Plan.ManifestPath); err != nil {
		return fmt.Errorf("clean manifest temporary files: %w", err)
	}

	if _, err := os.Lstat(record.Plan.ManifestPath); errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(record.Plan.WorkRoot)
		if readErr != nil {
			return fmt.Errorf("inspect Work root contents: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("Work root contains data but its manifest is missing")
		}
		if err := work.CreateJSON(record.Plan.ManifestPath, expected, 0o644); err != nil {
			return fmt.Errorf("create Work manifest: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect Work manifest: %w", err)
	}
	if err := verifyManifest(record.Plan.ManifestPath, expected); err != nil {
		return err
	}
	if !record.WorkRootReady {
		record.WorkRootReady = true
		record.Phase = PhaseCreatingRepositoryWorktrees
		record.UpdatedAt = e.now().UTC()
		if err := e.Store.Save(*record); err != nil {
			return fmt.Errorf("checkpoint Work root: %w", err)
		}
	}
	return nil
}

func (e Executor) continueRepositories(ctx context.Context, record *OperationRecord) error {
	record.Phase = PhaseCreatingRepositoryWorktrees
	for i := range record.Plan.Repositories {
		plan := record.Plan.Repositories[i]
		checkpoint := &record.Repositories[i]
		if err := ctx.Err(); err != nil {
			_, failure := e.fail(record, problemFromError(codeFor(err, CodeInterrupted), "cancel-between-repositories", plan.ID, plan.Destination, err))
			return failure
		}
		if checkpoint.State == StepVerified {
			facts, err := e.inspectRepositoryStep(ctx, plan)
			if err != nil {
				_, failure := e.fail(record, problemFromError(codeFor(err, CodeStateConflict), "reinspect-verified-worktree", plan.ID, plan.Destination, err))
				return failure
			}
			if err := facts.verify(plan, checkpoint.HeadOID, false); err != nil {
				_, failure := e.fail(record, problemFromError(CodeStateConflict, "reinspect-verified-worktree", plan.ID, plan.Destination, err))
				return failure
			}
			continue
		}
		if err := e.revalidateRepositorySourceAndBase(ctx, plan); err != nil {
			_, failure := e.fail(record, problemFromError(codeFor(err, CodeStateConflict), "revalidate-repository-source", plan.ID, plan.SourcePath, err))
			return failure
		}

		freshIntent := checkpoint.State == StepPending
		if freshIntent {
			checkpoint.State = StepIntent
			record.LastProblem = nil
			record.UpdatedAt = e.now().UTC()
			if err := e.Store.Save(*record); err != nil {
				_, failure := e.fail(record, problemFromError(CodeInternal, "record-repository-intent", plan.ID, record.Plan.OperationRecordPath, err))
				return failure
			}
		}

		facts, err := e.inspectRepositoryStep(ctx, plan)
		if err != nil {
			_, failure := e.fail(record, problemFromError(codeFor(err, CodeStateConflict), "inspect-repository-step", plan.ID, plan.Destination, err))
			return failure
		}
		if freshIntent && !facts.blank() {
			_, failure := e.fail(record, problemFromError(CodeStateConflict, "revalidate-repository-step", plan.ID, plan.Destination, facts.conflict(plan)))
			return failure
		}
		if facts.verify(plan, plan.BaseOID, false) == nil {
			if err := e.checkpointRepository(record, i, plan.BaseOID); err != nil {
				return err
			}
			continue
		}

		var commandErr error
		switch {
		case facts.blank():
			commandErr = e.Git.CreateWorktreeAtOID(ctx, plan.SourcePath, plan.Destination, plan.TargetBranchRef, plan.BaseOID)
		case facts.attachable(plan):
			commandErr = e.Git.AttachWorktree(ctx, plan.SourcePath, plan.Destination, plan.TargetBranchRef)
		default:
			_, failure := e.fail(record, problemFromError(CodeStateConflict, "reconcile-repository-step", plan.ID, plan.Destination, facts.conflict(plan)))
			return failure
		}
		if commandErr != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				_, failure := e.fail(record, problemFromError(codeFor(contextErr, CodeInterrupted), "create-repository-worktree", plan.ID, plan.Destination, contextErr))
				return failure
			}
			after, inspectErr := e.inspectRepositoryStep(ctx, plan)
			if inspectErr == nil && after.verify(plan, plan.BaseOID, false) == nil {
				if err := e.checkpointRepository(record, i, plan.BaseOID); err != nil {
					return err
				}
				continue
			}
			var cause error
			if inspectErr != nil {
				cause = errors.Join(commandErr, inspectErr)
			} else {
				cause = errors.Join(commandErr, after.conflict(plan))
			}
			_, failure := e.fail(record, problemFromError(codeFor(commandErr, CodeExternalFailure), "create-repository-worktree", plan.ID, plan.Destination, cause))
			return failure
		}

		after, err := e.inspectRepositoryStep(ctx, plan)
		if err != nil {
			_, failure := e.fail(record, problemFromError(codeFor(err, CodeStateConflict), "verify-repository-worktree", plan.ID, plan.Destination, err))
			return failure
		}
		if err := after.verify(plan, plan.BaseOID, false); err != nil {
			_, failure := e.fail(record, problemFromError(CodeStateConflict, "verify-repository-worktree", plan.ID, plan.Destination, err))
			return failure
		}
		if err := e.checkpointRepository(record, i, plan.BaseOID); err != nil {
			return err
		}
	}
	return nil
}

func (e Executor) continueHarness(record *OperationRecord) error {
	if record.Harness.State == StepVerified {
		return e.Harness.Verify(record.Plan, record.Harness)
	}
	record.Phase = PhaseGeneratingHarness
	record.Harness.State = StepIntent
	record.LastProblem = nil
	record.UpdatedAt = e.now().UTC()
	if err := e.Store.Save(*record); err != nil {
		return fmt.Errorf("checkpoint harness intent: %w", err)
	}
	checkpoint, err := e.Harness.Ensure(record.Plan)
	if err != nil {
		return err
	}
	now := e.now().UTC()
	checkpoint.State = StepVerified
	checkpoint.VerifiedAt = &now
	record.Harness = checkpoint
	record.UpdatedAt = now
	if err := e.Store.Save(*record); err != nil {
		return fmt.Errorf("checkpoint harness: %w", err)
	}
	return nil
}

func (e Executor) revalidateRepositorySourceAndBase(ctx context.Context, plan RepositoryPlan) error {
	identity, err := e.Git.InspectRepository(ctx, plan.SourcePath)
	if err != nil {
		return err
	}
	if identity.SourcePath != plan.SourcePath || identity.CommonDir != plan.GitCommonDir {
		return fmt.Errorf("source repository identity changed")
	}
	if err := e.Git.CommitExists(ctx, plan.SourcePath, plan.BaseOID); err != nil {
		return fmt.Errorf("planned base commit is unavailable: %w", err)
	}
	return nil
}

func (e Executor) checkpointRepository(record *OperationRecord, index int, headOID string) error {
	now := e.now().UTC()
	record.Repositories[index].State = StepVerified
	record.Repositories[index].HeadOID = headOID
	record.Repositories[index].VerifiedAt = &now
	record.LastProblem = nil
	record.UpdatedAt = now
	if err := e.Store.Save(*record); err != nil {
		_, failure := e.fail(record, problemFromError(CodeInternal, "checkpoint-repository-worktree", record.Repositories[index].ID, record.Plan.OperationRecordPath, err))
		return failure
	}
	return nil
}

func (e Executor) verifyCompleted(ctx context.Context, record OperationRecord, allowHeadMovement bool) error {
	if !record.WorkRootReady {
		return fmt.Errorf("Work root is not checkpointed")
	}
	if err := verifyManifest(record.Plan.ManifestPath, manifestFromOperation(record)); err != nil {
		return err
	}
	for i, plan := range record.Plan.Repositories {
		checkpoint := record.Repositories[i]
		if checkpoint.State != StepVerified {
			return fmt.Errorf("repository %q is not verified", plan.ID)
		}
		facts, err := e.inspectRepositoryStep(ctx, plan)
		if err != nil {
			return fmt.Errorf("repository %q: %w", plan.ID, err)
		}
		expectedHead := checkpoint.HeadOID
		if allowHeadMovement {
			expectedHead = ""
		}
		if err := facts.verify(plan, expectedHead, allowHeadMovement); err != nil {
			return fmt.Errorf("repository %q: %w", plan.ID, err)
		}
	}
	return e.Harness.Verify(record.Plan, record.Harness)
}

func (e Executor) revalidateInitialPlan(ctx context.Context, plan Plan) error {
	if _, err := os.Lstat(plan.OperationRecordPath); err == nil {
		return problems(Problem{Code: CodeAlreadyExists, Operation: "revalidate-operation-record", Path: plan.OperationRecordPath, Cause: fmt.Errorf("New Work operation already exists; Resume it")})
	} else if !errors.Is(err, os.ErrNotExist) {
		return problems(Problem{Code: CodeInternal, Operation: "revalidate-operation-record", Path: plan.OperationRecordPath, Cause: err})
	}
	if _, err := os.Lstat(plan.WorkRoot); err == nil {
		return problems(Problem{Code: CodeAlreadyExists, Operation: "revalidate-work-root", Path: plan.WorkRoot, Cause: fmt.Errorf("Work root exists")})
	} else if !errors.Is(err, os.ErrNotExist) {
		return problems(Problem{Code: CodeInternal, Operation: "revalidate-work-root", Path: plan.WorkRoot, Cause: err})
	}
	for _, repo := range plan.Repositories {
		identity, err := e.Git.InspectRepository(ctx, repo.SourcePath)
		if err != nil {
			return problems(Problem{Code: codeFor(err, CodeStateConflict), Operation: "revalidate-source", RepositoryID: repo.ID, Path: repo.SourcePath, Cause: err})
		}
		if identity.SourcePath != repo.SourcePath || identity.CommonDir != repo.GitCommonDir {
			return problems(Problem{Code: CodeStateConflict, Operation: "revalidate-source", RepositoryID: repo.ID, Path: repo.SourcePath, Cause: fmt.Errorf("repository identity changed")})
		}
		if err := e.Git.CommitExists(ctx, repo.SourcePath, repo.BaseOID); err != nil {
			return problems(Problem{Code: codeFor(err, CodeStateConflict), Operation: "revalidate-base-oid", RepositoryID: repo.ID, Cause: err})
		}
		facts, err := e.inspectRepositoryStep(ctx, repo)
		if err != nil {
			return problems(Problem{Code: codeFor(err, CodeStateConflict), Operation: "revalidate-repository-targets", RepositoryID: repo.ID, Cause: err})
		}
		if !facts.blank() {
			return problems(Problem{Code: CodeStateConflict, Operation: "revalidate-repository-targets", RepositoryID: repo.ID, Path: repo.Destination, Cause: facts.conflict(repo)})
		}
	}
	return nil
}

type repositoryStepFacts struct {
	branchExists            bool
	branchOID               string
	destinationExists       bool
	destinationMode         os.FileMode
	checkout                *gitops.Checkout
	destinationRegistration *gitops.WorktreeRegistration
	branchRegistrations     []gitops.WorktreeRegistration
}

func (e Executor) inspectRepositoryStep(ctx context.Context, plan RepositoryPlan) (repositoryStepFacts, error) {
	var facts repositoryStepFacts
	oid, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.TargetBranchRef)
	if err != nil {
		return facts, err
	}
	facts.branchExists = exists
	facts.branchOID = oid
	registrations, err := e.Git.ListWorktrees(ctx, plan.SourcePath)
	if err != nil {
		return facts, err
	}
	for i := range registrations {
		registration := registrations[i]
		if registration.Branch == plan.TargetBranchRef {
			facts.branchRegistrations = append(facts.branchRegistrations, registration)
		}
		if sameCleanPath(registration.Path, plan.Destination) {
			copy := registration
			facts.destinationRegistration = &copy
		}
	}
	info, err := os.Lstat(plan.Destination)
	if errors.Is(err, os.ErrNotExist) {
		return facts, nil
	}
	if err != nil {
		return facts, err
	}
	facts.destinationExists = true
	facts.destinationMode = info.Mode()
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return facts, nil
	}
	checkout, err := e.Git.InspectCheckout(ctx, plan.Destination)
	if err != nil {
		return facts, fmt.Errorf("destination is not the expected Git checkout: %w", err)
	}
	facts.checkout = &checkout
	return facts, nil
}

func (f repositoryStepFacts) blank() bool {
	return !f.branchExists && !f.destinationExists && f.destinationRegistration == nil && len(f.branchRegistrations) == 0
}

func (f repositoryStepFacts) attachable(plan RepositoryPlan) bool {
	return f.branchExists && f.branchOID == plan.BaseOID && !f.destinationExists && f.destinationRegistration == nil && len(f.branchRegistrations) == 0
}

func (f repositoryStepFacts) verify(plan RepositoryPlan, expectedHead string, allowHeadMovement bool) error {
	if !f.branchExists {
		return fmt.Errorf("planned branch is missing")
	}
	if !f.destinationExists || f.checkout == nil {
		return fmt.Errorf("planned checkout is missing")
	}
	if f.destinationRegistration == nil {
		return fmt.Errorf("planned worktree registration is missing")
	}
	if len(f.branchRegistrations) != 1 || !sameCleanPath(f.branchRegistrations[0].Path, plan.Destination) {
		return fmt.Errorf("planned branch registration is ambiguous")
	}
	if f.checkout.Identity.CommonDir != plan.GitCommonDir {
		return fmt.Errorf("checkout Git identity changed")
	}
	if f.checkout.FullRef != plan.TargetBranchRef || f.checkout.Detached {
		return fmt.Errorf("checkout ref is %q, expected %q", f.checkout.FullRef, plan.TargetBranchRef)
	}
	if f.destinationRegistration.Branch != plan.TargetBranchRef {
		return fmt.Errorf("registration ref is %q, expected %q", f.destinationRegistration.Branch, plan.TargetBranchRef)
	}
	if f.branchOID != f.checkout.HeadOID || f.branchOID != f.destinationRegistration.HeadOID {
		return fmt.Errorf("branch, checkout, and registration HEAD disagree")
	}
	if !allowHeadMovement && expectedHead != "" && f.branchOID != expectedHead {
		return fmt.Errorf("branch moved from expected commit %s to %s", expectedHead, f.branchOID)
	}
	return nil
}

func (f repositoryStepFacts) conflict(plan RepositoryPlan) error {
	return fmt.Errorf("observed target state conflicts with branch %s at %s and destination %s", plan.TargetBranchRef, displayOID(f.branchOID), plan.Destination)
}

func validatePlanShape(plan Plan) error {
	if plan.WorkID == "" || plan.WorkName == "" || plan.WorkRoot == "" || plan.ManifestPath == "" || plan.OperationRecordPath == "" {
		return fmt.Errorf("Work-level plan fields are incomplete")
	}
	if !plan.NoRemoteMutation {
		return fmt.Errorf("plan does not prohibit remote mutation")
	}
	if plan.Mode != ModeOnline && plan.Mode != ModeOffline {
		return fmt.Errorf("plan has invalid mode %q", plan.Mode)
	}
	if filepath.Clean(plan.ManifestPath) != filepath.Join(filepath.Clean(plan.WorkRoot), ".goworktree.json") {
		return fmt.Errorf("manifest path is outside the Work contract")
	}
	if filepath.Base(plan.OperationRecordPath) != plan.WorkID.String()+".json" {
		return fmt.Errorf("operation record path does not match Work identity")
	}
	if filepath.Base(filepath.Dir(plan.OperationRecordPath)) != "new-work" ||
		filepath.Base(filepath.Dir(filepath.Dir(plan.OperationRecordPath))) != "operations" {
		return fmt.Errorf("operation record path has invalid layout")
	}
	if !filepath.IsAbs(plan.WorkRoot) || !filepath.IsAbs(plan.ManifestPath) || !filepath.IsAbs(plan.OperationRecordPath) {
		return fmt.Errorf("Work paths must be absolute")
	}
	if filepath.Base(filepath.Clean(plan.WorkRoot)) != plan.WorkName.String() {
		return fmt.Errorf("Work root name does not match Work name")
	}
	if withinPath(plan.WorkRoot, plan.OperationRecordPath) {
		return fmt.Errorf("operation record must be external to Work root")
	}
	worksRoot := filepath.Dir(filepath.Clean(plan.WorkRoot))
	if work.NewIdentity(worksRoot, plan.WorkName) != plan.WorkID {
		return fmt.Errorf("Work identity does not match root and name")
	}
	if len(plan.Repositories) == 0 {
		return fmt.Errorf("plan has no repositories")
	}
	seenIDs := make(map[string]struct{}, len(plan.Repositories))
	seenCommon := make(map[string]struct{}, len(plan.Repositories))
	previousID := ""
	for _, repo := range plan.Repositories {
		if repo.ID == "" || repo.SourcePath == "" || repo.GitCommonDir == "" || repo.BaseRef == "" || repo.BaseOID == "" || repo.Destination == "" {
			return fmt.Errorf("repository plan is incomplete")
		}
		if !filepath.IsAbs(repo.SourcePath) || !filepath.IsAbs(repo.GitCommonDir) || !filepath.IsAbs(repo.Destination) {
			return fmt.Errorf("repository %q paths must be absolute", repo.ID)
		}
		if previousID != "" && repo.ID < previousID {
			return fmt.Errorf("repository plans are not in stable ID order")
		}
		previousID = repo.ID
		if _, exists := seenIDs[repo.ID]; exists {
			return fmt.Errorf("repository ID %q is duplicated", repo.ID)
		}
		seenIDs[repo.ID] = struct{}{}
		if _, exists := seenCommon[repo.GitCommonDir]; exists {
			return fmt.Errorf("Git common directory %q is duplicated", repo.GitCommonDir)
		}
		seenCommon[repo.GitCommonDir] = struct{}{}
		if repo.TargetBranchRef != "refs/heads/"+plan.WorkName.String() {
			return fmt.Errorf("repository %q target branch does not match Work name", repo.ID)
		}
		if filepath.Dir(filepath.Clean(repo.Destination)) != filepath.Clean(plan.WorkRoot) {
			return fmt.Errorf("repository %q destination is not a direct Work child", repo.ID)
		}
		decoded := make([]byte, len(repo.BaseOID)/2)
		if len(repo.BaseOID) != 40 && len(repo.BaseOID) != 64 {
			return fmt.Errorf("repository %q has invalid base OID", repo.ID)
		}
		if _, err := hex.Decode(decoded, []byte(repo.BaseOID)); err != nil {
			return fmt.Errorf("repository %q has invalid base OID", repo.ID)
		}
	}
	return nil
}

func verifyManifest(path string, expected work.Manifest) error {
	var actual work.Manifest
	if err := work.LoadJSON(path, &actual); err != nil {
		return fmt.Errorf("load Work manifest: %w", err)
	}
	if err := actual.Validate(); err != nil {
		return fmt.Errorf("validate Work manifest: %w", err)
	}
	// Schema 1 and schema 2 revision-zero manifests describe the same immutable
	// New Work intent. This keeps interrupted schema-1 operations resumable
	// after the application starts creating schema-2 manifests.
	if actual.SchemaVersion == work.LegacyManifestSchemaVersion && expected.Revision == 0 {
		actual.SchemaVersion = work.ManifestSchemaVersion
	}
	actualJSON, actualErr := json.Marshal(actual)
	expectedJSON, expectedErr := json.Marshal(expected)
	if actualErr != nil || expectedErr != nil {
		return fmt.Errorf("encode Work manifest for comparison: %w", errors.Join(actualErr, expectedErr))
	}
	if !bytes.Equal(actualJSON, expectedJSON) {
		return fmt.Errorf("Work manifest does not match recorded intent")
	}
	return nil
}

func (e Executor) withLocks(ctx context.Context, plan Plan, run func() (ExecutionResult, error)) (ExecutionResult, error) {
	identities := make([]string, 0, len(plan.Repositories))
	for _, repo := range plan.Repositories {
		identities = append(identities, repo.GitCommonDir)
	}
	sort.Strings(identities)
	release, err := e.Locker.AcquireExecution(ctx, plan.WorkID.String(), identities)
	if err != nil {
		code := codeFor(err, CodeInternal)
		if errors.Is(err, lockops.ErrLocked) {
			code = CodeLocked
		}
		return ExecutionResult{}, problems(Problem{Code: code, Operation: "acquire-execution-locks", Cause: err})
	}
	result, runErr := run()
	if releaseErr := release(); releaseErr != nil {
		lockErr := problems(Problem{Code: CodeInternal, Operation: "release-execution-locks", Cause: releaseErr})
		if runErr == nil {
			return result, lockErr
		}
		return result, errors.Join(runErr, lockErr)
	}
	return result, runErr
}

func (e Executor) validateDependencies() *Problem {
	if e.Git == nil {
		return &Problem{Code: CodeInternal, Operation: "initialize-executor", Cause: fmt.Errorf("Git adapter is nil")}
	}
	if e.Locker == nil {
		return &Problem{Code: CodeInternal, Operation: "initialize-executor", Cause: fmt.Errorf("execution locker is nil")}
	}
	if e.Store == nil {
		return &Problem{Code: CodeInternal, Operation: "initialize-executor", Cause: fmt.Errorf("operation store is nil")}
	}
	if e.Harness == nil {
		return &Problem{Code: CodeInternal, Operation: "initialize-executor", Cause: fmt.Errorf("harness manager is nil")}
	}
	return nil
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Executor) newID() (string, error) {
	if e.NewID != nil {
		return e.NewID()
	}
	return randomOperationID()
}

func (e Executor) fail(record *OperationRecord, problem Problem) (ExecutionResult, error) {
	switch problem.Code {
	case CodeInterrupted, CodeTimeout:
		record.Phase = PhaseInterrupted
	default:
		record.Phase = PhasePartial
	}
	record.LastProblem = &PersistedProblem{
		Code:         problem.Code,
		Operation:    problem.Operation,
		RepositoryID: problem.RepositoryID,
		Path:         problem.Path,
		Message:      errorMessage(problem.Cause),
	}
	record.UpdatedAt = e.now().UTC()
	if err := e.Store.Save(*record); err != nil {
		problem.Cause = errors.Join(problem.Cause, fmt.Errorf("persist failure checkpoint: %w", err))
	}
	return resultFromRecord(*record, statusForCode(problem.Code)), problems(problem)
}

func problemFromError(code ErrorCode, operation, repositoryID, path string, err error) Problem {
	return Problem{Code: code, Operation: operation, RepositoryID: repositoryID, Path: path, Cause: err}
}

func resultFromRecord(record OperationRecord, status ExecutionStatus) ExecutionResult {
	result := ExecutionResult{Status: status, WorkRoot: record.Plan.WorkRoot, OperationRecordPath: record.Plan.OperationRecordPath}
	for _, checkpoint := range record.Repositories {
		if checkpoint.State == StepVerified {
			result.VerifiedRepositories = append(result.VerifiedRepositories, checkpoint.ID)
		}
	}
	return result
}

func statusForError(err error) ExecutionStatus {
	var problemsError *ProblemsError
	if errors.As(err, &problemsError) {
		for _, problem := range problemsError.Problems {
			if problem.Code == CodeInterrupted || problem.Code == CodeTimeout {
				return ExecutionInterrupted
			}
		}
	}
	return ExecutionPartial
}

func statusForCode(code ErrorCode) ExecutionStatus {
	if code == CodeInterrupted || code == CodeTimeout {
		return ExecutionInterrupted
	}
	return ExecutionPartial
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func displayOID(oid string) string {
	if oid == "" {
		return "<absent>"
	}
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}

func withinPath(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
