package inspectwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

type Git interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
}

type OperationReader interface {
	Load(string) (newwork.OperationRecord, error)
}

type Inspector struct {
	Git        Git
	Operations OperationReader
	Now        func() time.Time
	Limiter    *RepositoryLimiter
}

// RepositoryLimiter bounds read-only repository inspections shared by one or
// more concurrent Work inspections.
type RepositoryLimiter struct {
	tokens chan struct{}
}

// NewRepositoryLimiter creates a limiter with at least one available slot.
func NewRepositoryLimiter(parallelism int) *RepositoryLimiter {
	return &RepositoryLimiter{tokens: make(chan struct{}, max(1, parallelism))}
}

func (l *RepositoryLimiter) acquire(ctx context.Context) (func(), error) {
	select {
	case l.tokens <- struct{}{}:
		return func() { <-l.tokens }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type SystemGit struct{}

func (SystemGit) InspectRepository(ctx context.Context, path string) (gitops.RepositoryIdentity, error) {
	return gitops.InspectRepositoryContext(ctx, path)
}

func (SystemGit) LocalBranchOID(ctx context.Context, repo, branch string) (string, bool, error) {
	return gitops.LocalBranchOIDContext(ctx, repo, branch)
}

func (SystemGit) ListWorktrees(ctx context.Context, repo string) ([]gitops.WorktreeRegistration, error) {
	return gitops.ListWorktreesContext(ctx, repo)
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

type SystemOperationReader struct{}

func (SystemOperationReader) Load(path string) (newwork.OperationRecord, error) {
	return (newwork.OperationStore{}).Load(path)
}

func (i Inspector) Inspect(ctx context.Context, request Request) (Snapshot, error) {
	if i.Git == nil {
		return Snapshot{}, fmt.Errorf("Inspect Work Git adapter is nil")
	}
	if i.Operations == nil {
		return Snapshot{}, fmt.Errorf("Inspect Work operation reader is nil")
	}
	if request.WorksRoot == "" {
		return Snapshot{}, fmt.Errorf("works root is empty")
	}
	if request.ControlRoot == "" {
		return Snapshot{}, fmt.Errorf("control root is empty")
	}
	name, err := work.ParseName(request.Name)
	if err != nil {
		return Snapshot{}, fmt.Errorf("validate Work name: %w", err)
	}
	worksRoot, err := canonicalOptionalDirectory(request.WorksRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect works root: %w", err)
	}
	controlRoot, err := canonicalOptionalDirectory(request.ControlRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect control root: %w", err)
	}
	if pathsOverlap(worksRoot, controlRoot) {
		return Snapshot{}, fmt.Errorf("control root and works root must not contain each other")
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	workRoot := filepath.Join(worksRoot, name.String())
	workID := work.NewIdentity(worksRoot, name)
	snapshot := Snapshot{
		InspectedAt: i.now().UTC(),
		WorkName:    name,
		WorkID:      workID,
		WorksRoot:   worksRoot,
		WorkRoot:    workRoot,
		Manifest: ManifestSnapshot{
			Path:  filepath.Join(workRoot, ".goworktree.json"),
			State: MetadataAbsent,
		},
		Operation: OperationSnapshot{
			Path:  filepath.Join(controlRoot, "operations", "new-work", workID.String()+".json"),
			State: MetadataAbsent,
		},
		Harness: HarnessSnapshot{Path: filepath.Join(workRoot, "go.work")},
	}

	snapshot.WorkRootKind = inspectPath(workRoot)
	var legacyIntents []work.RepositoryIntent
	switch snapshot.WorkRootKind {
	case PathMissing:
		snapshot.addProblem(Problem{
			Code: ProblemWorkRootMissing, Path: workRoot,
			Message: "Work root does not exist", Next: ActionRepairWork,
		})
	case PathDirectory:
		legacyIntents = i.inspectManifest(&snapshot)
	case PathSymlink, PathRegular, PathOther, PathUnknown:
		snapshot.addProblem(Problem{
			Code: ProblemWorkRootUnsafe, Path: workRoot,
			Message: "Work root is not a real directory", Next: ActionRepairWork,
		})
	}

	record, recordValid := i.inspectOperation(&snapshot)
	manifestValid := snapshot.Manifest.State == MetadataValid && snapshot.Manifest.Value != nil
	linked := false
	if manifestValid && recordValid {
		if err := matchManifestAndOperation(*snapshot.Manifest.Value, record); err != nil {
			snapshot.addProblem(Problem{
				Code: ProblemOperationMismatch, Path: snapshot.Operation.Path,
				Message: err.Error(), Next: ActionRepairWork,
			})
		} else {
			linked = true
		}
	}
	classifyNewWorkRecovery(&snapshot, record, recordValid, manifestValid, linked)

	var intents []work.RepositoryIntent
	switch {
	case manifestValid:
		snapshot.IntentSource = IntentManifest
		intents = append(intents, snapshot.Manifest.Value.Repositories...)
	case snapshot.Manifest.State == MetadataLegacy:
		snapshot.IntentSource = IntentLegacyManifest
		intents = append(intents, legacyIntents...)
	case recordValid:
		snapshot.IntentSource = IntentOperationRecord
		intents = intentsFromOperation(record)
	default:
		snapshot.IntentSource = IntentNone
	}
	repositories, err := i.inspectRepositories(ctx, intents, snapshot.IntentSource, snapshot.Operation.ResumeSuggested)
	for _, repository := range repositories {
		snapshot.Repositories = append(snapshot.Repositories, repository)
		snapshot.Problems = append(snapshot.Problems, repository.Problems...)
	}
	if err != nil {
		return snapshot, err
	}

	i.inspectHarness(&snapshot, record, recordValid && (linked || !manifestValid))
	return snapshot, nil
}

func (i Inspector) inspectRepositories(
	ctx context.Context,
	intents []work.RepositoryIntent,
	source IntentSource,
	resumeSuggested bool,
) ([]RepositorySnapshot, error) {
	if i.Limiter == nil || len(intents) < 2 {
		results := make([]RepositorySnapshot, 0, len(intents))
		for _, intent := range intents {
			if err := ctx.Err(); err != nil {
				return results, err
			}
			results = append(results, i.inspectRepository(ctx, intent, source, resumeSuggested))
		}
		return results, nil
	}

	results := make([]RepositorySnapshot, len(intents))
	var inspections sync.WaitGroup
	var contextErr error
	for index := range intents {
		release, err := i.Limiter.acquire(ctx)
		if err != nil {
			contextErr = err
			break
		}
		inspections.Add(1)
		go func() {
			defer inspections.Done()
			defer release()
			results[index] = i.inspectRepository(ctx, intents[index], source, resumeSuggested)
		}()
	}
	inspections.Wait()
	if contextErr == nil {
		contextErr = ctx.Err()
	}
	return results, contextErr
}

func (i Inspector) inspectManifest(snapshot *Snapshot) []work.RepositoryIntent {
	var manifest work.Manifest
	err := work.LoadJSON(snapshot.Manifest.Path, &manifest)
	if errors.Is(err, os.ErrNotExist) {
		snapshot.addProblem(Problem{
			Code: ProblemManifestMissing, Path: snapshot.Manifest.Path,
			Message: "Work directory exists but its manifest is missing", Next: ActionRepairWork,
		})
		return nil
	}
	if err != nil {
		legacyIntents, legacyErr := loadLegacyManifest(snapshot.Manifest.Path, *snapshot)
		if legacyErr == nil {
			snapshot.Manifest.State = MetadataLegacy
			return legacyIntents
		}
		snapshot.Manifest.State = MetadataInvalid
		snapshot.addProblem(Problem{
			Code: ProblemManifestInvalid, Path: snapshot.Manifest.Path,
			Message: errors.Join(err, legacyErr).Error(), Next: ActionRepairWork,
		})
		return nil
	}
	if err := manifest.Validate(); err != nil {
		snapshot.Manifest.State = MetadataInvalid
		snapshot.addProblem(Problem{
			Code: ProblemManifestInvalid, Path: snapshot.Manifest.Path,
			Message: err.Error(), Next: ActionRepairWork,
		})
		return nil
	}
	if err := validateManifestScope(manifest, *snapshot); err != nil {
		snapshot.Manifest.State = MetadataInvalid
		snapshot.addProblem(Problem{
			Code: ProblemWorkIdentityMismatch, Path: snapshot.Manifest.Path,
			Message: err.Error(), Next: ActionRepairWork,
		})
		return nil
	}
	snapshot.Manifest.State = MetadataValid
	snapshot.Manifest.Value = &manifest
	return nil
}

func (i Inspector) inspectOperation(snapshot *Snapshot) (newwork.OperationRecord, bool) {
	record, err := i.Operations.Load(snapshot.Operation.Path)
	if errors.Is(err, os.ErrNotExist) {
		if snapshot.Manifest.State != MetadataLegacy && (snapshot.WorkRootKind != PathMissing || snapshot.Manifest.State != MetadataAbsent) {
			snapshot.addProblem(Problem{
				Code: ProblemOperationMissing, Path: snapshot.Operation.Path,
				Message: "New Work operation record is missing", Next: ActionRepairWork,
			})
		}
		return newwork.OperationRecord{}, false
	}
	if err != nil {
		snapshot.Operation.State = MetadataInvalid
		snapshot.addProblem(Problem{
			Code: ProblemOperationInvalid, Path: snapshot.Operation.Path,
			Message: err.Error(), Next: ActionRepairWork,
		})
		return newwork.OperationRecord{}, false
	}
	snapshot.Operation.State = MetadataValid
	snapshot.Operation.OperationID = record.OperationID
	snapshot.Operation.Phase = string(record.Phase)
	updated := record.UpdatedAt
	snapshot.Operation.UpdatedAt = &updated
	if record.LastProblem != nil {
		snapshot.Operation.LastProblem = record.LastProblem.Message
	}
	if record.WorkID != snapshot.WorkID || record.Plan.WorkName != snapshot.WorkName ||
		filepath.Clean(record.Plan.WorkRoot) != filepath.Clean(snapshot.WorkRoot) ||
		filepath.Clean(record.Plan.ManifestPath) != filepath.Clean(snapshot.Manifest.Path) {
		snapshot.Operation.State = MetadataInvalid
		snapshot.addProblem(Problem{
			Code: ProblemOperationMismatch, Path: snapshot.Operation.Path,
			Message: "operation record does not belong to the requested Work", Next: ActionRepairWork,
		})
		return record, false
	}
	return record, true
}

func classifyNewWorkRecovery(snapshot *Snapshot, record newwork.OperationRecord, recordValid, manifestValid, linked bool) {
	if !recordValid || record.Phase == newwork.PhaseCreated {
		return
	}
	manifestAbsentAtSafeRoot := snapshot.Manifest.State == MetadataAbsent &&
		(snapshot.WorkRootKind == PathMissing || snapshot.WorkRootKind == PathDirectory)
	resumeAllowed := (manifestValid && linked) || (!manifestValid && manifestAbsentAtSafeRoot)
	next := ActionRepairWork
	if resumeAllowed {
		snapshot.Operation.ResumeSuggested = true
		next = ActionResumeNewWork
		for index := range snapshot.Problems {
			if snapshot.Problems[index].Code == ProblemWorkRootMissing {
				snapshot.Problems[index].Next = ActionResumeNewWork
			}
		}
	}
	snapshot.addProblem(Problem{
		Code: ProblemNewWorkIncomplete, Path: snapshot.Operation.Path,
		Message: fmt.Sprintf("New Work operation is in phase %q", record.Phase), Next: next,
	})
}

func (i Inspector) inspectRepository(ctx context.Context, intent work.RepositoryIntent, source IntentSource, resumeSuggested bool) RepositorySnapshot {
	repository := RepositorySnapshot{
		ID:              intent.ID,
		Intent:          intent,
		IntentSource:    source,
		DestinationKind: inspectPath(intent.Destination),
	}
	next := ActionRepairWork
	if resumeSuggested {
		next = ActionResumeNewWork
	}

	identity, err := i.Git.InspectRepository(ctx, intent.SourcePath)
	if err != nil {
		repository.addProblem(Problem{
			Code: ProblemSourceUnreadable, Path: intent.SourcePath,
			Message: err.Error(), Next: ActionRepairWork,
		})
	} else {
		repository.SourceKnown = true
		repository.Source = RepositoryIdentity{SourcePath: identity.SourcePath, CommonDir: identity.CommonDir}
		if source != IntentLegacyManifest && (identity.SourcePath != intent.SourcePath || identity.CommonDir != intent.GitCommonDir) {
			repository.addProblem(Problem{
				Code: ProblemSourceIdentityMismatch, Path: intent.SourcePath,
				Message: "configured source resolves to a different Git repository", Next: ActionRepairWork,
			})
		}
	}

	if repository.SourceKnown {
		oid, exists, branchErr := i.Git.LocalBranchOID(ctx, intent.SourcePath, intent.BranchRef)
		if branchErr != nil {
			repository.addProblem(Problem{
				Code: ProblemBranchInspectionFailed, Path: intent.SourcePath,
				Message: branchErr.Error(), Next: ActionRepairWork,
			})
		} else {
			repository.BranchKnown = true
			repository.BranchExists = exists
			repository.BranchOID = oid
			if !exists {
				repository.addProblem(Problem{
					Code: ProblemBranchMissing, Path: intent.SourcePath,
					Message: fmt.Sprintf("expected local branch %s is missing", intent.BranchRef), Next: next,
				})
			}
		}

		registrations, registrationErr := i.Git.ListWorktrees(ctx, intent.SourcePath)
		if registrationErr != nil {
			repository.addProblem(Problem{
				Code: ProblemRegistrationInspection, Path: intent.SourcePath,
				Message: registrationErr.Error(), Next: ActionRepairWork,
			})
		} else {
			repository.RegistrationsKnown = true
			for _, registration := range registrations {
				value := registrationSnapshot(registration)
				if registration.Branch == intent.BranchRef {
					repository.BranchRegistrations = append(repository.BranchRegistrations, value)
				}
				if samePath(registration.Path, intent.Destination) {
					copy := value
					repository.DestinationRegistration = &copy
				}
			}
		}
	}

	switch repository.DestinationKind {
	case PathMissing:
		repository.addProblem(Problem{
			Code: ProblemDestinationMissing, Path: intent.Destination,
			Message: "expected worktree directory is missing", Next: next,
		})
	case PathDirectory:
		checkout, checkoutErr := i.Git.InspectCheckout(ctx, intent.Destination)
		if checkoutErr != nil {
			repository.addProblem(Problem{
				Code: ProblemDestinationNotWorktree, Path: intent.Destination,
				Message: checkoutErr.Error(), Next: ActionRepairWork,
			})
		} else {
			repository.CheckoutKnown = true
			repository.Checkout = CheckoutSnapshot{
				SourcePath: checkout.Identity.SourcePath,
				CommonDir:  checkout.Identity.CommonDir,
				FullRef:    checkout.FullRef,
				HeadOID:    checkout.HeadOID,
				Detached:   checkout.Detached,
			}
			if intent.GitCommonDir != "" && checkout.Identity.CommonDir != intent.GitCommonDir {
				repository.addProblem(Problem{
					Code: ProblemCheckoutIdentityMismatch, Path: intent.Destination,
					Message: "worktree belongs to a different Git repository", Next: ActionRepairWork,
				})
			}
			if checkout.FullRef != intent.BranchRef || checkout.Detached {
				repository.addProblem(Problem{
					Code: ProblemBranchRefMismatch, Path: intent.Destination,
					Message: fmt.Sprintf("checkout ref is %q, expected %q", checkout.FullRef, intent.BranchRef), Next: ActionRepairWork,
				})
			}
			i.inspectWorkingTree(ctx, &repository)
		}
	case PathSymlink, PathRegular, PathOther, PathUnknown:
		repository.addProblem(Problem{
			Code: ProblemDestinationUnsafe, Path: intent.Destination,
			Message: "worktree path is not a real directory", Next: ActionRepairWork,
		})
	}

	i.reconcileRegistration(&repository, next)
	i.reconcileHeads(&repository)
	return repository
}

func (i Inspector) inspectWorkingTree(ctx context.Context, repository *RepositorySnapshot) {
	status, err := i.Git.WorkingTreeStatus(ctx, repository.Intent.Destination)
	if err != nil {
		repository.addProblem(Problem{
			Code: ProblemWorkingTreeUnknown, Path: repository.Intent.Destination,
			Message: err.Error(), Next: ActionRepairWork,
		})
	} else {
		repository.WorkingTreeKnown = true
		repository.WorkingTree = status
	}
	operations, err := i.Git.ActiveOperations(ctx, repository.Intent.Destination)
	if err != nil {
		repository.addProblem(Problem{
			Code: ProblemGitOperationUnknown, Path: repository.Intent.Destination,
			Message: err.Error(), Next: ActionRepairWork,
		})
		return
	}
	repository.GitOperationsKnown = true
	repository.GitOperations = operations
	for _, operation := range operations {
		repository.addProblem(Problem{
			Code: ProblemActiveGitOperation, Path: repository.Intent.Destination,
			Message: fmt.Sprintf("Git %s operation is active", operation), Next: ActionResolveGitOperation,
		})
	}
}

func (i Inspector) reconcileRegistration(repository *RepositorySnapshot, next Action) {
	if !repository.RegistrationsKnown {
		return
	}
	destinationExists := repository.DestinationKind != PathMissing
	if repository.DestinationRegistration == nil && destinationExists {
		repository.addProblem(Problem{
			Code: ProblemRegistrationMissing, Path: repository.Intent.Destination,
			Message: "directory exists but Git has no worktree registration for it", Next: ActionRepairWork,
		})
	}
	if repository.DestinationRegistration != nil && !destinationExists {
		repository.addProblem(Problem{
			Code: ProblemRegistrationStale, Path: repository.Intent.Destination,
			Message: "Git worktree registration exists but its directory is missing", Next: next,
		})
	}
	if repository.DestinationRegistration != nil && repository.DestinationRegistration.Branch != repository.Intent.BranchRef {
		repository.addProblem(Problem{
			Code: ProblemRegistrationConflict, Path: repository.Intent.Destination,
			Message: fmt.Sprintf("destination is registered for %q, expected %q", repository.DestinationRegistration.Branch, repository.Intent.BranchRef), Next: ActionRepairWork,
		})
	}
	if repository.BranchKnown && repository.BranchExists &&
		(len(repository.BranchRegistrations) != 1 ||
			len(repository.BranchRegistrations) == 1 && !samePath(repository.BranchRegistrations[0].Path, repository.Intent.Destination)) {
		repository.addProblem(Problem{
			Code: ProblemRegistrationConflict, Path: repository.Intent.Destination,
			Message: fmt.Sprintf("expected branch has %d matching worktree registrations", len(repository.BranchRegistrations)), Next: ActionRepairWork,
		})
	}
}

func (i Inspector) reconcileHeads(repository *RepositorySnapshot) {
	var observed []string
	if repository.BranchKnown && repository.BranchExists {
		observed = append(observed, repository.BranchOID)
	}
	if repository.CheckoutKnown {
		observed = append(observed, repository.Checkout.HeadOID)
	}
	if repository.DestinationRegistration != nil {
		observed = append(observed, repository.DestinationRegistration.HeadOID)
	}
	if len(observed) < 2 {
		return
	}
	for _, oid := range observed[1:] {
		if oid != observed[0] {
			repository.addProblem(Problem{
				Code: ProblemHeadMismatch, Path: repository.Intent.Destination,
				Message: "branch, checkout, and worktree registration disagree on HEAD", Next: ActionRepairWork,
			})
			return
		}
	}
}

func (i Inspector) inspectHarness(snapshot *Snapshot, record newwork.OperationRecord, verifyOperation bool) {
	snapshot.Harness.Kind = inspectPath(snapshot.Harness.Path)
	if snapshot.IntentSource == IntentLegacyManifest {
		return
	}
	expectsHarness := len(record.Plan.HarnessUsePaths) > 0
	if snapshot.Manifest.Value != nil && len(snapshot.Manifest.Value.Harness.UsePaths) > 0 {
		expectsHarness = true
	}
	for _, repository := range snapshot.Repositories {
		if repository.Intent.IncludeInGoWork {
			expectsHarness = true
			break
		}
	}
	if verifyOperation && record.Harness.State == newwork.StepVerified {
		if err := (newwork.GoWorkHarness{}).Verify(record.Plan, record.Harness); err != nil {
			snapshot.addProblem(Problem{
				Code: ProblemHarnessMismatch, Path: snapshot.Harness.Path,
				Message: err.Error(), Next: ActionRepairWork,
			})
			return
		}
		snapshot.Harness.VerifiedAgainstOperation = true
		return
	}
	if snapshot.Operation.ResumeSuggested && snapshot.Harness.Kind == PathMissing {
		return
	}
	if expectsHarness && snapshot.Harness.Kind == PathMissing {
		snapshot.addProblem(Problem{
			Code: ProblemHarnessMissing, Path: snapshot.Harness.Path,
			Message: "manifest expects go.work but the file is missing", Next: recoveryAction(snapshot.Operation.ResumeSuggested),
		})
	}
	if !expectsHarness && snapshot.Harness.Kind != PathMissing && snapshot.Harness.Kind != PathUnknown {
		snapshot.addProblem(Problem{
			Code: ProblemHarnessUnexpected, Path: snapshot.Harness.Path,
			Message: "go.work exists although no repository root module is intended", Next: ActionRepairWork,
		})
	}
}

type legacyManifest struct {
	Name      string                   `json:"name"`
	CreatedAt string                   `json:"created_at"`
	Repos     []legacyRepositoryIntent `json:"repos"`
}

type legacyRepositoryIntent struct {
	ID     string `json:"id"`
	Folder string `json:"folder"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func loadLegacyManifest(path string, snapshot Snapshot) ([]work.RepositoryIntent, error) {
	var manifest legacyManifest
	if err := work.LoadJSON(path, &manifest); err != nil {
		return nil, err
	}
	if manifest.Name != snapshot.WorkName.String() {
		return nil, fmt.Errorf("legacy manifest name %q does not match Work directory", manifest.Name)
	}
	if manifest.CreatedAt == "" {
		return nil, fmt.Errorf("legacy manifest creation time is empty")
	}
	if _, err := time.Parse(time.RFC3339, manifest.CreatedAt); err != nil {
		return nil, fmt.Errorf("legacy manifest creation time: %w", err)
	}
	if len(manifest.Repos) == 0 {
		return nil, fmt.Errorf("legacy manifest has no repositories")
	}
	seenIDs := make(map[string]struct{}, len(manifest.Repos))
	seenDestinations := make(map[string]struct{}, len(manifest.Repos))
	intents := make([]work.RepositoryIntent, 0, len(manifest.Repos))
	for index, repository := range manifest.Repos {
		if repository.ID == "" {
			return nil, fmt.Errorf("legacy manifest repository %d has empty ID", index)
		}
		if _, exists := seenIDs[repository.ID]; exists {
			return nil, fmt.Errorf("legacy manifest repository ID %q is duplicated", repository.ID)
		}
		seenIDs[repository.ID] = struct{}{}
		folder := repository.Folder
		if folder == "" {
			folder = repository.ID
		}
		if _, err := work.ParseName(folder); err != nil {
			return nil, fmt.Errorf("legacy manifest repository %q folder: %w", repository.ID, err)
		}
		if !filepath.IsAbs(repository.Path) {
			return nil, fmt.Errorf("legacy manifest repository %q source path is not absolute", repository.ID)
		}
		destination := filepath.Join(snapshot.WorkRoot, folder)
		if _, exists := seenDestinations[destination]; exists {
			return nil, fmt.Errorf("legacy manifest destination %q is duplicated", destination)
		}
		seenDestinations[destination] = struct{}{}
		branch := repository.Branch
		if branch == "" {
			return nil, fmt.Errorf("legacy manifest repository %q branch is empty", repository.ID)
		}
		if !strings.HasPrefix(branch, "refs/heads/") {
			branch = "refs/heads/" + branch
		}
		intents = append(intents, work.RepositoryIntent{
			ID: repository.ID, SourcePath: filepath.Clean(repository.Path), BaseRef: repository.Base,
			BranchRef: branch, Destination: destination,
		})
	}
	return intents, nil
}

func validateManifestScope(manifest work.Manifest, snapshot Snapshot) error {
	if manifest.WorkID != snapshot.WorkID || manifest.Name != snapshot.WorkName {
		return fmt.Errorf("manifest Work identity does not match its directory")
	}
	previousID := ""
	for _, repository := range manifest.Repositories {
		if previousID != "" && repository.ID < previousID {
			return fmt.Errorf("manifest repositories are not in stable ID order")
		}
		previousID = repository.ID
		if !filepath.IsAbs(repository.SourcePath) || !filepath.IsAbs(repository.GitCommonDir) || !filepath.IsAbs(repository.Destination) {
			return fmt.Errorf("repository %q has non-absolute paths", repository.ID)
		}
		if repository.BranchRef != "refs/heads/"+manifest.Name.String() {
			return fmt.Errorf("repository %q branch does not match Work name", repository.ID)
		}
		if filepath.Dir(filepath.Clean(repository.Destination)) != filepath.Clean(snapshot.WorkRoot) {
			return fmt.Errorf("repository %q destination is outside Work root", repository.ID)
		}
	}
	return nil
}

func matchManifestAndOperation(manifest work.Manifest, record newwork.OperationRecord) error {
	if manifest.WorkID != record.WorkID || manifest.NewWorkOperationID != record.OperationID || manifest.Name != record.Plan.WorkName {
		return fmt.Errorf("manifest and operation record identities disagree")
	}
	if len(manifest.Repositories) != len(record.Plan.Repositories) {
		return fmt.Errorf("manifest and operation record repository counts disagree")
	}
	for index, intended := range manifest.Repositories {
		planned := record.Plan.Repositories[index]
		if intended.ID != planned.ID || intended.SourcePath != planned.SourcePath ||
			intended.GitCommonDir != planned.GitCommonDir || intended.BaseRef != planned.BaseRef ||
			intended.BaseOID != planned.BaseOID || intended.BranchRef != planned.TargetBranchRef ||
			intended.Destination != planned.Destination || intended.IncludeInGoWork != planned.IncludeInGoWork {
			return fmt.Errorf("manifest and operation record disagree on repository %q", intended.ID)
		}
	}
	rootModulesOnly := len(record.Plan.HarnessUsePaths) == 0
	if manifest.Harness.RootModulesOnly != rootModulesOnly || !slices.Equal(manifest.Harness.UsePaths, record.Plan.HarnessUsePaths) {
		return fmt.Errorf("manifest and operation record disagree on harness use paths")
	}
	return nil
}

func intentsFromOperation(record newwork.OperationRecord) []work.RepositoryIntent {
	intents := make([]work.RepositoryIntent, 0, len(record.Plan.Repositories))
	for _, repository := range record.Plan.Repositories {
		intents = append(intents, work.RepositoryIntent{
			ID:              repository.ID,
			SourcePath:      repository.SourcePath,
			GitCommonDir:    repository.GitCommonDir,
			BaseRef:         repository.BaseRef,
			BaseOID:         repository.BaseOID,
			BranchRef:       repository.TargetBranchRef,
			Destination:     repository.Destination,
			IncludeInGoWork: repository.IncludeInGoWork,
		})
	}
	return intents
}

func registrationSnapshot(registration gitops.WorktreeRegistration) RegistrationSnapshot {
	return RegistrationSnapshot{
		Path: registration.Path, HeadOID: registration.HeadOID, Branch: registration.Branch,
		Detached: registration.Detached, Locked: registration.Locked, Prunable: registration.Prunable,
	}
}

func inspectPath(path string) PathKind {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return PathMissing
	}
	if err != nil {
		return PathUnknown
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return PathSymlink
	}
	if info.IsDir() {
		return PathDirectory
	}
	if info.Mode().IsRegular() {
		return PathRegular
	}
	return PathOther
}

func canonicalOptionalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(abs), nil
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", canonical)
	}
	return filepath.Clean(canonical), nil
}

func pathsOverlap(a, b string) bool {
	return within(a, b) || within(b, a)
}

func within(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}

func recoveryAction(resume bool) Action {
	if resume {
		return ActionResumeNewWork
	}
	return ActionRepairWork
}

func (s *Snapshot) addProblem(problem Problem) {
	s.Problems = append(s.Problems, problem)
}

func (r *RepositorySnapshot) addProblem(problem Problem) {
	problem.RepositoryID = r.ID
	r.Problems = append(r.Problems, problem)
}

func (i Inspector) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now()
}
