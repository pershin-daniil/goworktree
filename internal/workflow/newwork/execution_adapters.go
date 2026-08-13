package newwork

import (
	"context"
	"errors"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
)

type ExecutionGit interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	CommitExists(context.Context, string, string) error
	CreateWorktreeAtOID(context.Context, string, string, string, string) error
	AttachWorktree(context.Context, string, string, string) error
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
}

func (SystemGit) CommitExists(ctx context.Context, repo, oid string) error {
	return gitops.CommitExistsContext(ctx, repo, oid)
}

func (SystemGit) CreateWorktreeAtOID(ctx context.Context, repo, destination, branchRef, oid string) error {
	return gitops.CreateWorktreeAtOIDContext(ctx, repo, destination, branchRef, oid)
}

func (SystemGit) AttachWorktree(ctx context.Context, repo, destination, branchRef string) error {
	return gitops.AttachWorktreeContext(ctx, repo, destination, branchRef)
}

func (SystemGit) InspectCheckout(ctx context.Context, path string) (gitops.Checkout, error) {
	return gitops.InspectCheckoutContext(ctx, path)
}

type ExecutionLocker interface {
	AcquireExecution(context.Context, string, []string) (func() error, error)
}

type FileExecutionLocker struct {
	Set lockops.Set
}

func (l FileExecutionLocker) AcquireExecution(ctx context.Context, workID string, repositoryIdentities []string) (func() error, error) {
	workLease, err := l.Set.Acquire(ctx, "works", []string{workID})
	if err != nil {
		return nil, err
	}
	repositoryLease, err := l.Set.Acquire(ctx, "repositories", repositoryIdentities)
	if err != nil {
		return nil, errors.Join(err, workLease.Release())
	}
	return func() error {
		return errors.Join(repositoryLease.Release(), workLease.Release())
	}, nil
}
