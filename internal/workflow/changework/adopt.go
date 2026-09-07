package changework

import (
	"context"
	"fmt"
	"os"
	"strings"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
)

// BuildAdopt records the currently checked-out local branch as Work intent.
// It never checks out, creates, deletes, or moves a Git ref.
func (p Planner) BuildAdopt(ctx context.Context, catalog Catalog, request AdoptRequest) (Plan, error) {
	if p.Git == nil {
		return Plan{}, fmt.Errorf("Change Work Git adapter is nil")
	}
	plan, err := p.basePlan(catalog, request.WorkName, []string{request.RepositoryID}, KindAdopt)
	if err != nil {
		return Plan{}, err
	}
	intent, exists := activeByID(plan.Before)[request.RepositoryID]
	if !exists {
		return Plan{}, fmt.Errorf("repository %q is not active in Work %q", request.RepositoryID, request.WorkName)
	}
	checkout, err := p.Git.InspectCheckout(ctx, intent.Destination)
	if err != nil {
		return Plan{}, fmt.Errorf("repository %s: inspect current branch: %w", intent.ID, err)
	}
	entry := RepositoryPlan{
		ID: intent.ID, SourcePath: intent.SourcePath, GitCommonDir: intent.GitCommonDir,
		BaseRef: intent.BaseRef, BaseOID: intent.BaseOID, BranchRef: checkout.FullRef,
		BranchOID: checkout.HeadOID, Destination: intent.Destination, IncludeInGoWork: intent.IncludeInGoWork,
	}
	if err := verifyAdoption(ctx, p.Git, entry); err != nil {
		return Plan{}, err
	}
	plan.Repositories = append(plan.Repositories, entry)
	for index := range plan.After.Repositories {
		if plan.After.Repositories[index].ID == intent.ID {
			plan.After.Repositories[index].BranchRef = entry.BranchRef
		}
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

type adoptionGit interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
}

func verifyAdoption(ctx context.Context, git adoptionGit, plan RepositoryPlan) error {
	if !strings.HasPrefix(plan.BranchRef, "refs/heads/") || plan.BranchRef == "refs/heads/" {
		return fmt.Errorf("repository %s: detached HEAD cannot be adopted", plan.ID)
	}
	info, err := os.Lstat(plan.Destination)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("repository %s: worktree must be a real directory", plan.ID)
	}
	identity, err := git.InspectRepository(ctx, plan.SourcePath)
	if err != nil {
		return fmt.Errorf("repository %s: inspect source: %w", plan.ID, err)
	}
	if identity.SourcePath != plan.SourcePath || identity.CommonDir != plan.GitCommonDir {
		return fmt.Errorf("repository %s: source identity changed", plan.ID)
	}
	checkout, err := git.InspectCheckout(ctx, plan.Destination)
	if err != nil {
		return fmt.Errorf("repository %s: inspect checkout: %w", plan.ID, err)
	}
	if checkout.Detached || checkout.Identity.CommonDir != plan.GitCommonDir || checkout.FullRef != plan.BranchRef || checkout.HeadOID != plan.BranchOID {
		return fmt.Errorf("repository %s: checkout identity, branch, or HEAD changed", plan.ID)
	}
	oid, exists, err := git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef)
	if err != nil {
		return fmt.Errorf("repository %s: inspect branch: %w", plan.ID, err)
	}
	if !exists || oid != plan.BranchOID {
		return fmt.Errorf("repository %s: current local branch does not match checkout HEAD", plan.ID)
	}
	registrations, err := git.ListWorktrees(ctx, plan.SourcePath)
	if err != nil {
		return fmt.Errorf("repository %s: inspect registrations: %w", plan.ID, err)
	}
	exact := 0
	for _, registration := range registrations {
		if samePath(registration.Path, plan.Destination) || registration.Branch == plan.BranchRef {
			if !samePath(registration.Path, plan.Destination) || registration.Branch != plan.BranchRef || registration.HeadOID != plan.BranchOID || registration.Detached || registration.Prunable {
				return fmt.Errorf("repository %s: current branch registration is ambiguous", plan.ID)
			}
			exact++
		}
	}
	if exact != 1 {
		return fmt.Errorf("repository %s: current branch must have one exact worktree registration", plan.ID)
	}
	operations, err := git.ActiveOperations(ctx, plan.Destination)
	if err != nil {
		return fmt.Errorf("repository %s: inspect Git operations: %w", plan.ID, err)
	}
	if len(operations) > 0 {
		return fmt.Errorf("repository %s: finish Git %s before adopting its branch", plan.ID, operations[0])
	}
	return nil
}

func (e Executor) ensureAdopted(ctx context.Context, record *OperationRecord, index int) error {
	if err := verifyAdoption(ctx, e.Git, record.Plan.Repositories[index]); err != nil {
		return err
	}
	if record.Repositories[index].State == StepDone {
		return nil
	}
	record.Repositories[index].State = StepDone
	return saveRecord(*record, e.now())
}
