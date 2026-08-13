package removework

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

const operationSchemaVersion = 1

type Entry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type RepositoryPlan struct {
	ID, SourcePath, GitCommonDir, Destination string
	BranchRef, BranchOID                      string
	WorkingTree                               gitops.WorkingTreeStatus
}

type Plan struct {
	WorkName        string
	WorkID          string
	WorkRoot        string
	OperationRecord string
	BuiltAt         time.Time
	Repositories    []RepositoryPlan
	RootEntries     []Entry
	Irreversible    bool
	RemoteMutation  bool
}

type RepositoryResult struct {
	ID, Status string
	Err        error
}

type Result struct {
	WorkName     string
	RootRemoved  bool
	Repositories []RepositoryResult
}

type Git interface {
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	RemoveWorktree(context.Context, string, string) error
	DeleteLocalBranchAtOID(context.Context, string, string, string) error
}

type SystemGit struct{}

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
func (SystemGit) LocalBranchOID(ctx context.Context, path, branch string) (string, bool, error) {
	return gitops.LocalBranchOIDContext(ctx, path, branch)
}
func (SystemGit) RemoveWorktree(ctx context.Context, source, destination string) error {
	return gitops.RemoveWorktreeContext(ctx, source, destination)
}
func (SystemGit) DeleteLocalBranchAtOID(ctx context.Context, source, ref, oid string) error {
	return gitops.DeleteLocalBranchAtOIDContext(ctx, source, ref, oid)
}

type Planner struct{ Now func() time.Time }

func (p Planner) Build(snapshot inspectwork.Snapshot, controlRoot string) (Plan, error) {
	operationPath := filepath.Join(controlRoot, "operations", "remove-work", snapshot.WorkID.String()+".json")
	var interrupted operationRecord
	if err := work.LoadJSON(operationPath, &interrupted); err == nil {
		if err := validateRecord(interrupted, snapshot.WorkID.String()); err != nil {
			return Plan{}, err
		}
		return interrupted.Plan, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Plan{}, fmt.Errorf("read Remove Work operation: %w", err)
	}
	if snapshot.Manifest.State != inspectwork.MetadataValid || snapshot.Manifest.Value == nil {
		return Plan{}, fmt.Errorf("Work manifest is not valid; run Repair Work")
	}
	if snapshot.WorkRootKind != inspectwork.PathDirectory {
		return Plan{}, fmt.Errorf("Work root is not a real directory; run Repair Work")
	}
	entries, err := inspectEntries(snapshot.WorkRoot, snapshot.Repositories)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		WorkName: snapshot.WorkName.String(), WorkID: snapshot.WorkID.String(), WorkRoot: snapshot.WorkRoot,
		OperationRecord: operationPath,
		BuiltAt:         p.now().UTC(), RootEntries: entries, Irreversible: true,
	}
	for _, repository := range snapshot.Repositories {
		if reason := repositoryBlock(repository); reason != "" {
			return Plan{}, fmt.Errorf("repository %s: %s", repository.ID, reason)
		}
		plan.Repositories = append(plan.Repositories, RepositoryPlan{
			ID: repository.ID, SourcePath: repository.Intent.SourcePath, GitCommonDir: repository.Intent.GitCommonDir,
			Destination: repository.Intent.Destination, BranchRef: repository.Intent.BranchRef,
			BranchOID: repository.BranchOID, WorkingTree: repository.WorkingTree,
		})
	}
	sort.Slice(plan.Repositories, func(i, j int) bool { return plan.Repositories[i].ID < plan.Repositories[j].ID })
	return plan, nil
}

func repositoryBlock(repository inspectwork.RepositorySnapshot) string {
	if !repository.SourceKnown || !repository.BranchKnown || !repository.BranchExists || !repository.CheckoutKnown || !repository.WorkingTreeKnown || !repository.GitOperationsKnown || !repository.RegistrationsKnown {
		return "managed identity cannot be proven; run Repair Work"
	}
	if len(repository.Problems) > 0 {
		return repository.Problems[0].Message
	}
	if len(repository.GitOperations) > 0 {
		return "active Git operation: " + string(repository.GitOperations[0])
	}
	return ""
}

func inspectEntries(root string, repositories []inspectwork.RepositorySnapshot) ([]Entry, error) {
	managed := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		managed[filepath.Clean(repository.Intent.Destination)] = struct{}{}
	}
	directory, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read Work root: %w", err)
	}
	entries := make([]Entry, 0, len(directory))
	for _, item := range directory {
		path := filepath.Join(root, item.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		kind := "other"
		switch {
		case info.Mode().IsRegular():
			kind = "file"
		case info.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
		case info.IsDir():
			kind = "directory"
			if _, isManaged := managed[filepath.Clean(path)]; !isManaged {
				if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
					return nil, fmt.Errorf("unmanaged nested Git repository %s blocks Remove Work", path)
				}
			}
		}
		entries = append(entries, Entry{Name: item.Name(), Kind: kind})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
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
	repoLease, err := l.Set.Acquire(ctx, "repositories", unique(repositories))
	if err != nil {
		return nil, errors.Join(err, workLease.Release())
	}
	return func() error { return errors.Join(repoLease.Release(), workLease.Release()) }, nil
}

type checkpoint struct {
	ID              string `json:"id"`
	WorktreeRemoved bool   `json:"worktree_removed"`
	BranchDeleted   bool   `json:"branch_deleted"`
}

type operationRecord struct {
	SchemaVersion int          `json:"schema_version"`
	Kind          string       `json:"kind"`
	Plan          Plan         `json:"plan"`
	Repositories  []checkpoint `json:"repositories"`
	RootRemoved   bool         `json:"root_removed"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type Executor struct {
	Git    Git
	Locker Locker
	Now    func() time.Time
}

func (e Executor) Execute(ctx context.Context, plan Plan, confirmation string) (result Result, returnErr error) {
	if confirmation != plan.WorkName {
		return result, fmt.Errorf("confirmation must exactly equal Work name %q", plan.WorkName)
	}
	if e.Git == nil || e.Locker == nil {
		return result, fmt.Errorf("Remove Work executor dependencies are incomplete")
	}
	identities := make([]string, 0, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		identities = append(identities, repository.GitCommonDir)
	}
	release, err := e.Locker.AcquireExecution(ctx, plan.WorkID, identities)
	if err != nil {
		return result, fmt.Errorf("acquire Remove Work locks: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, release()) }()
	record, err := e.loadOrCreate(plan)
	if err != nil {
		return result, err
	}
	result.WorkName = plan.WorkName
	for index, repository := range plan.Repositories {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		outcome := RepositoryResult{ID: repository.ID}
		if !record.Repositories[index].WorktreeRemoved {
			if err := e.revalidateWorktree(ctx, repository); err != nil {
				outcome.Status, outcome.Err = "state-conflict", err
				result.Repositories = append(result.Repositories, outcome)
				return result, fmt.Errorf("Remove Work stopped after target changed: %w", err)
			}
			if err := e.Git.RemoveWorktree(ctx, repository.SourcePath, repository.Destination); err != nil {
				outcome.Status, outcome.Err = "failed", err
				result.Repositories = append(result.Repositories, outcome)
				return result, err
			}
			record.Repositories[index].WorktreeRemoved = true
			if err := e.save(record); err != nil {
				return result, err
			}
		}
		if !record.Repositories[index].BranchDeleted {
			oid, exists, err := e.Git.LocalBranchOID(ctx, repository.SourcePath, repository.BranchRef)
			if err != nil || !exists || oid != repository.BranchOID {
				return result, fmt.Errorf("branch %s changed after confirmation", repository.BranchRef)
			}
			if err := e.Git.DeleteLocalBranchAtOID(ctx, repository.SourcePath, repository.BranchRef, repository.BranchOID); err != nil {
				return result, err
			}
			record.Repositories[index].BranchDeleted = true
			if err := e.save(record); err != nil {
				return result, err
			}
		}
		outcome.Status = "removed"
		result.Repositories = append(result.Repositories, outcome)
	}
	entries, err := inspectEntries(plan.WorkRoot, nil)
	if err != nil {
		return result, err
	}
	if !sameEntries(entries, remainingEntries(plan.RootEntries, plan.Repositories)) {
		return result, fmt.Errorf("Work root contents changed after confirmation")
	}
	if err := os.RemoveAll(plan.WorkRoot); err != nil {
		return result, fmt.Errorf("remove Work root: %w", err)
	}
	if _, err := os.Lstat(plan.WorkRoot); !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("Work root still exists after removal")
	}
	record.RootRemoved = true
	if err := e.save(record); err != nil {
		return result, err
	}
	result.RootRemoved = true
	return result, nil
}

func (e Executor) revalidateWorktree(ctx context.Context, plan RepositoryPlan) error {
	checkout, err := e.Git.InspectCheckout(ctx, plan.Destination)
	if err != nil {
		return err
	}
	if checkout.Identity.CommonDir != plan.GitCommonDir || checkout.FullRef != plan.BranchRef || checkout.HeadOID != plan.BranchOID {
		return fmt.Errorf("repository %s checkout identity, branch, or HEAD changed", plan.ID)
	}
	status, err := e.Git.WorkingTreeStatus(ctx, plan.Destination)
	if err != nil || status != plan.WorkingTree {
		return fmt.Errorf("repository %s working tree changed", plan.ID)
	}
	operations, err := e.Git.ActiveOperations(ctx, plan.Destination)
	if err != nil || len(operations) != 0 {
		return fmt.Errorf("repository %s has an active Git operation", plan.ID)
	}
	registrations, err := e.Git.ListWorktrees(ctx, plan.SourcePath)
	if err != nil {
		return err
	}
	for _, registration := range registrations {
		if filepath.Clean(registration.Path) == filepath.Clean(plan.Destination) && registration.Branch == plan.BranchRef && registration.HeadOID == plan.BranchOID {
			return nil
		}
	}
	return fmt.Errorf("repository %s worktree registration changed", plan.ID)
}

func (e Executor) loadOrCreate(plan Plan) (operationRecord, error) {
	var record operationRecord
	err := work.LoadJSON(plan.OperationRecord, &record)
	if err == nil {
		if err := validateRecord(record, plan.WorkID); err != nil {
			return record, err
		}
		if record.Plan.WorkName != plan.WorkName {
			return record, fmt.Errorf("Remove Work operation record does not match this Work")
		}
		return record, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return record, err
	}
	record = operationRecord{SchemaVersion: operationSchemaVersion, Kind: "remove-work", Plan: plan, UpdatedAt: e.now().UTC()}
	for _, repository := range plan.Repositories {
		record.Repositories = append(record.Repositories, checkpoint{ID: repository.ID})
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		return record, err
	}
	if err := work.CreateJSON(plan.OperationRecord, record, 0o600); err != nil {
		return record, err
	}
	return record, nil
}

func validateRecord(record operationRecord, workID string) error {
	if record.SchemaVersion != operationSchemaVersion || record.Kind != "remove-work" || record.Plan.WorkID != workID {
		return fmt.Errorf("Remove Work operation record is invalid")
	}
	if len(record.Repositories) != len(record.Plan.Repositories) {
		return fmt.Errorf("Remove Work operation checkpoints do not match plan")
	}
	for index := range record.Repositories {
		if record.Repositories[index].ID != record.Plan.Repositories[index].ID {
			return fmt.Errorf("Remove Work operation checkpoint %d does not match plan", index)
		}
	}
	return nil
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

func remainingEntries(entries []Entry, repositories []RepositoryPlan) []Entry {
	removed := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		removed[filepath.Base(repository.Destination)] = struct{}{}
	}
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if _, ok := removed[entry.Name]; !ok {
			result = append(result, entry)
		}
	}
	return result
}

func sameEntries(left, right []Entry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func unique(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && (len(result) == 0 || result[len(result)-1] != value) {
			result = append(result, value)
		}
	}
	return result
}
