package repairwork

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

type ActionKind string

const (
	ActionReattachWorktree   ActionKind = "reattach-worktree"
	ActionRepairRegistration ActionKind = "repair-registration"
	ActionRegenerateHarness  ActionKind = "regenerate-harness"
	ActionRestoreOperation   ActionKind = "restore-operation-record"
)

type Action struct {
	Kind         ActionKind
	RepositoryID string
	SourcePath   string
	GitCommonDir string
	Destination  string
	BranchRef    string
	BranchOID    string
	Description  string
}

type Plan struct {
	WorkName      string
	WorkID        string
	WorkRoot      string
	OperationPath string
	Manifest      work.Manifest
	Actions       []Action
}

type ActionResult struct {
	Kind         ActionKind
	RepositoryID string
	Status       string
}

type Result struct {
	WorkName string
	Actions  []ActionResult
}

type Planner struct{}

func (Planner) Build(snapshot inspectwork.Snapshot) (Plan, error) {
	if snapshot.Manifest.State != inspectwork.MetadataValid || snapshot.Manifest.Value == nil {
		return Plan{}, fmt.Errorf("Repair Work will not infer intent from an invalid or legacy manifest")
	}
	plan := Plan{
		WorkName: snapshot.WorkName.String(), WorkID: snapshot.WorkID.String(), WorkRoot: snapshot.WorkRoot,
		OperationPath: snapshot.Operation.Path, Manifest: *snapshot.Manifest.Value,
	}
	consumedGlobal := make(map[inspectwork.ProblemCode]bool)
	if snapshot.Harness.Kind == inspectwork.PathMissing && hasProblem(snapshot.Problems, inspectwork.ProblemHarnessMissing) {
		plan.Actions = append(plan.Actions, Action{Kind: ActionRegenerateHarness, Description: "recreate the exact go.work declared by the manifest"})
		consumedGlobal[inspectwork.ProblemHarnessMissing] = true
	}
	if snapshot.Operation.State == inspectwork.MetadataAbsent && hasProblem(snapshot.Problems, inspectwork.ProblemOperationMissing) {
		plan.Actions = append(plan.Actions, Action{Kind: ActionRestoreOperation, Description: "reconstruct the completed New Work record from the valid manifest"})
		consumedGlobal[inspectwork.ProblemOperationMissing] = true
	}
	for _, repository := range snapshot.Repositories {
		action, consumed, ok := repositoryAction(repository)
		if ok {
			plan.Actions = append(plan.Actions, action)
		}
		for _, problem := range repository.Problems {
			if !consumed[problem.Code] {
				return Plan{}, fmt.Errorf("repository %s is ambiguous: [%s] %s", repository.ID, problem.Code, problem.Message)
			}
		}
	}
	for _, problem := range snapshot.Problems {
		if problem.RepositoryID == "" && !consumedGlobal[problem.Code] {
			return Plan{}, fmt.Errorf("Repair Work cannot safely automate [%s] %s", problem.Code, problem.Message)
		}
	}
	sort.SliceStable(plan.Actions, func(i, j int) bool {
		return actionOrder(plan.Actions[i]) < actionOrder(plan.Actions[j])
	})
	return plan, nil
}

func repositoryAction(repository inspectwork.RepositorySnapshot) (Action, map[inspectwork.ProblemCode]bool, bool) {
	consumed := make(map[inspectwork.ProblemCode]bool)
	base := Action{
		RepositoryID: repository.ID, SourcePath: repository.Intent.SourcePath,
		GitCommonDir: repository.Intent.GitCommonDir, Destination: repository.Intent.Destination,
		BranchRef: repository.Intent.BranchRef, BranchOID: repository.BranchOID,
	}
	if repository.SourceKnown && repository.BranchKnown && repository.BranchExists && repository.RegistrationsKnown &&
		repository.DestinationKind == inspectwork.PathMissing && repository.DestinationRegistration != nil &&
		!repository.DestinationRegistration.Locked && repository.DestinationRegistration.Branch == repository.Intent.BranchRef &&
		repository.DestinationRegistration.HeadOID == repository.BranchOID && len(repository.BranchRegistrations) == 1 {
		base.Kind = ActionReattachWorktree
		base.Description = "prune the stale registration and attach the existing exact local branch"
		consumed[inspectwork.ProblemDestinationMissing] = true
		consumed[inspectwork.ProblemRegistrationStale] = true
		return base, consumed, true
	}
	if repository.SourceKnown && repository.BranchKnown && repository.BranchExists && repository.RegistrationsKnown &&
		repository.DestinationKind == inspectwork.PathDirectory && repository.DestinationRegistration == nil &&
		repository.CheckoutKnown && repository.Checkout.CommonDir == repository.Intent.GitCommonDir &&
		repository.Checkout.FullRef == repository.Intent.BranchRef && repository.Checkout.HeadOID == repository.BranchOID &&
		repository.GitOperationsKnown && len(repository.GitOperations) == 0 {
		base.Kind = ActionRepairRegistration
		base.Description = "rebuild Git administrative registration for the proven existing checkout"
		consumed[inspectwork.ProblemRegistrationMissing] = true
		consumed[inspectwork.ProblemRegistrationConflict] = true
		return base, consumed, true
	}
	return Action{}, consumed, false
}

func hasProblem(problems []inspectwork.Problem, code inspectwork.ProblemCode) bool {
	for _, problem := range problems {
		if problem.Code == code {
			return true
		}
	}
	return false
}

func actionOrder(action Action) string {
	switch action.Kind {
	case ActionReattachWorktree, ActionRepairRegistration:
		return "1-" + action.RepositoryID
	case ActionRegenerateHarness:
		return "2"
	default:
		return "3"
	}
}

type Git interface {
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	PruneAndAttach(context.Context, string, string, string) error
	RepairRegistration(context.Context, string, string) error
}

type SystemGit struct{}

func (SystemGit) LocalBranchOID(ctx context.Context, source, branch string) (string, bool, error) {
	return gitops.LocalBranchOIDContext(ctx, source, branch)
}
func (SystemGit) ListWorktrees(ctx context.Context, source string) ([]gitops.WorktreeRegistration, error) {
	return gitops.ListWorktreesContext(ctx, source)
}
func (SystemGit) InspectCheckout(ctx context.Context, destination string) (gitops.Checkout, error) {
	return gitops.InspectCheckoutContext(ctx, destination)
}
func (SystemGit) PruneAndAttach(ctx context.Context, source, destination, branch string) error {
	return gitops.PruneAndAttachWorktreeContext(ctx, source, destination, branch)
}
func (SystemGit) RepairRegistration(ctx context.Context, source, destination string) error {
	return gitops.RepairWorktreeRegistrationContext(ctx, source, destination)
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
	if len(repositories) == 0 {
		return workLease.Release, nil
	}
	repoLease, err := l.Set.Acquire(ctx, "repositories", unique(repositories))
	if err != nil {
		return nil, errors.Join(err, workLease.Release())
	}
	return func() error { return errors.Join(repoLease.Release(), workLease.Release()) }, nil
}

type Executor struct {
	Git    Git
	Locker Locker
	Store  newwork.OperationStore
	Now    func() time.Time
}

func (e Executor) Execute(ctx context.Context, plan Plan) (result Result, returnErr error) {
	if e.Git == nil || e.Locker == nil {
		return result, fmt.Errorf("Repair Work executor dependencies are incomplete")
	}
	identities := make([]string, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		if action.GitCommonDir != "" {
			identities = append(identities, action.GitCommonDir)
		}
	}
	release, err := e.Locker.AcquireExecution(ctx, plan.WorkID, identities)
	if err != nil {
		return result, fmt.Errorf("acquire Repair Work locks: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, release()) }()
	result.WorkName = plan.WorkName
	for _, action := range plan.Actions {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := e.executeAction(ctx, plan, action); err != nil {
			return result, fmt.Errorf("%s %s: %w", action.Kind, action.RepositoryID, err)
		}
		result.Actions = append(result.Actions, ActionResult{Kind: action.Kind, RepositoryID: action.RepositoryID, Status: "repaired"})
	}
	return result, nil
}

func (e Executor) executeAction(ctx context.Context, plan Plan, action Action) error {
	switch action.Kind {
	case ActionReattachWorktree:
		if _, err := os.Lstat(action.Destination); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("destination appeared after planning")
		}
		if err := e.verifyBranch(ctx, action); err != nil {
			return err
		}
		if err := e.Git.PruneAndAttach(ctx, action.SourcePath, action.Destination, action.BranchRef); err != nil {
			return err
		}
		return e.verifyCheckout(ctx, action)
	case ActionRepairRegistration:
		if err := e.verifyBranch(ctx, action); err != nil {
			return err
		}
		if err := e.verifyCheckout(ctx, action); err != nil {
			return err
		}
		if err := e.Git.RepairRegistration(ctx, action.SourcePath, action.Destination); err != nil {
			return err
		}
		return e.verifyRegistration(ctx, action)
	case ActionRegenerateHarness:
		newPlan, err := newwork.PlanFromManifest(plan.Manifest, plan.WorkRoot, plan.OperationPath)
		if err != nil {
			return err
		}
		checkpoint, err := (newwork.GoWorkHarness{}).Ensure(newPlan)
		if err != nil {
			return err
		}
		checkpoint.State = newwork.StepVerified
		verified := e.now().UTC()
		checkpoint.VerifiedAt = &verified
		return (newwork.GoWorkHarness{}).Verify(newPlan, checkpoint)
	case ActionRestoreOperation:
		record, err := newwork.RecoverCompletedOperation(plan.Manifest, plan.WorkRoot, plan.OperationPath, e.now())
		if err != nil {
			return err
		}
		if err := e.Store.Create(record); err != nil {
			return err
		}
		_, err = e.Store.Load(plan.OperationPath)
		return err
	default:
		return fmt.Errorf("unknown repair action %q", action.Kind)
	}
}

func (e Executor) verifyBranch(ctx context.Context, action Action) error {
	oid, exists, err := e.Git.LocalBranchOID(ctx, action.SourcePath, action.BranchRef)
	if err != nil || !exists || oid != action.BranchOID {
		return fmt.Errorf("local branch changed after planning")
	}
	return nil
}

func (e Executor) verifyCheckout(ctx context.Context, action Action) error {
	checkout, err := e.Git.InspectCheckout(ctx, action.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != action.GitCommonDir || checkout.FullRef != action.BranchRef || checkout.HeadOID != action.BranchOID {
		return fmt.Errorf("checkout identity changed after planning")
	}
	return nil
}

func (e Executor) verifyRegistration(ctx context.Context, action Action) error {
	registrations, err := e.Git.ListWorktrees(ctx, action.SourcePath)
	if err != nil {
		return err
	}
	for _, registration := range registrations {
		if filepath.Clean(registration.Path) == filepath.Clean(action.Destination) && registration.Branch == action.BranchRef && registration.HeadOID == action.BranchOID {
			return nil
		}
	}
	return fmt.Errorf("expected worktree registration was not restored")
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func unique(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value != "" && (len(result) == 0 || result[len(result)-1] != value) {
			result = append(result, value)
		}
	}
	return result
}
