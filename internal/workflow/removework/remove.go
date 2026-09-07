package removework

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	archiveops "github.com/pershin-daniil/goworktree/internal/archive"
	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/syncwork"
)

const (
	operationSchemaVersion = 3
	fingerprintVersion     = 1
)

type StepState string

const (
	StepPending   StepState = "pending"
	StepIntent    StepState = "intent"
	StepCompleted StepState = "completed"
)

type Phase string

const (
	PhaseWorktrees Phase = "removing-worktrees"
	PhaseBranches  Phase = "removing-branches"
	PhaseRoot      Phase = "removing-root"
	PhaseArchiving Phase = "archiving"
)

type Entry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type RepositoryPlan struct {
	ID, SourcePath, GitCommonDir, Destination string
	BranchRef, BranchOID                      string
	WorkingTree                               gitops.WorkingTreeStatus
	WorkingTreeFingerprint                    string
	// BranchOnly is a retained inactive Work branch. It has no worktree to
	// remove, but still belongs to the confirmed Remove Work operation.
	BranchOnly bool `json:"branch_only,omitempty"`
}

type Plan struct {
	WorkName           string
	WorkID             string
	WorkRoot           string
	OperationRecord    string
	BuiltAt            time.Time
	Repositories       []RepositoryPlan
	RootEntries        []Entry
	RootFingerprint    string
	FingerprintVersion int
	Irreversible       bool
	RemoteMutation     bool
}

type RepositoryResult struct {
	ID, Status string
	Err        error
}

type Result struct {
	WorkName     string
	RootRemoved  bool
	ArchiveID    string
	ArchivePath  string
	Repositories []RepositoryResult
}

type Git interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorktreeFingerprint(context.Context, string) (string, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	RemoveWorktree(context.Context, string, string) error
	DeleteLocalBranchAtOID(context.Context, string, string, string) error
}

type SystemGit struct{}

func (SystemGit) InspectRepository(ctx context.Context, path string) (gitops.RepositoryIdentity, error) {
	return gitops.InspectRepositoryContext(ctx, path)
}
func (SystemGit) InspectCheckout(ctx context.Context, path string) (gitops.Checkout, error) {
	return gitops.InspectCheckoutContext(ctx, path)
}
func (SystemGit) WorktreeFingerprint(ctx context.Context, path string) (string, error) {
	return gitops.WorktreeFingerprintContext(ctx, path)
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

type Planner struct {
	Git Git
	Now func() time.Time
}

func (p Planner) Build(ctx context.Context, snapshot inspectwork.Snapshot, controlRoot string) (Plan, error) {
	operationPath := filepath.Join(controlRoot, "operations", "remove-work", snapshot.WorkID.String()+".json")
	if interrupted, err := loadRecord(operationPath); err == nil {
		migrated, err := migrateAndValidate(interrupted, snapshot.WorkID.String())
		if err != nil {
			return Plan{}, err
		}
		return migrated.Plan, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Plan{}, fmt.Errorf("read Remove Work operation: %w", err)
	}
	syncPath := filepath.Join(controlRoot, "operations", "sync-work", snapshot.WorkID.String()+".json")
	if exists, terminal, err := syncwork.OperationTerminal(syncPath, snapshot.WorkID.String()); err != nil {
		return Plan{}, fmt.Errorf("inspect Sync Work operation before removal: %w", err)
	} else if exists && !terminal {
		return Plan{}, fmt.Errorf("unfinished Sync Work operation blocks Remove Work; Resume Sync first")
	}
	if snapshot.Operation.State != inspectwork.MetadataValid {
		return Plan{}, fmt.Errorf("New Work operation record is not valid; run Repair Work before Remove Work")
	}
	if snapshot.Operation.Phase != string(newwork.PhaseCreated) {
		return Plan{}, fmt.Errorf("New Work operation is not complete; Resume New Work before Remove Work")
	}
	if snapshot.Manifest.State != inspectwork.MetadataValid || snapshot.Manifest.Value == nil {
		return Plan{}, fmt.Errorf("Work manifest is not valid; run Repair Work")
	}
	if snapshot.WorkRootKind != inspectwork.PathDirectory {
		return Plan{}, fmt.Errorf("Work root is not a real directory; run Repair Work")
	}
	entries, rootFingerprint, err := inspectEntries(snapshot.WorkRoot, snapshot.Repositories)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		WorkName: snapshot.WorkName.String(), WorkID: snapshot.WorkID.String(), WorkRoot: snapshot.WorkRoot,
		OperationRecord: operationPath, BuiltAt: p.now().UTC(), RootEntries: entries,
		RootFingerprint: rootFingerprint, FingerprintVersion: fingerprintVersion, Irreversible: true,
	}
	git := p.git()
	for _, repository := range snapshot.Repositories {
		if reason := repositoryBlock(repository); reason != "" {
			return Plan{}, fmt.Errorf("repository %s: %s", repository.ID, reason)
		}
		fingerprint, err := git.WorktreeFingerprint(ctx, repository.Intent.Destination)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: fingerprint working tree: %w", repository.ID, err)
		}
		plan.Repositories = append(plan.Repositories, RepositoryPlan{
			ID: repository.ID, SourcePath: repository.Intent.SourcePath, GitCommonDir: repository.Intent.GitCommonDir,
			Destination: repository.Intent.Destination, BranchRef: repository.Intent.BranchRef,
			BranchOID: repository.BranchOID, WorkingTree: repository.WorkingTree,
			WorkingTreeFingerprint: fingerprint,
		})
	}
	for _, inactive := range snapshot.Manifest.Value.InactiveRepositories {
		if !inactive.BranchRetained {
			continue
		}
		intent := inactive.Repository
		identity, err := git.InspectRepository(ctx, intent.SourcePath)
		if err != nil {
			return Plan{}, fmt.Errorf("retained repository %s: inspect source identity: %w", intent.ID, err)
		}
		if identity.SourcePath != intent.SourcePath || identity.CommonDir != intent.GitCommonDir {
			return Plan{}, fmt.Errorf("retained repository %s source identity changed", intent.ID)
		}
		oid, exists, err := git.LocalBranchOID(ctx, intent.SourcePath, intent.BranchRef)
		if err != nil {
			return Plan{}, fmt.Errorf("retained repository %s: inspect local branch: %w", intent.ID, err)
		}
		if exists && oid != inactive.BranchOID {
			return Plan{}, fmt.Errorf("retained repository %s branch changed after it was removed from Work", intent.ID)
		}
		registrations, err := git.ListWorktrees(ctx, intent.SourcePath)
		if err != nil {
			return Plan{}, fmt.Errorf("retained repository %s: inspect worktree registrations: %w", intent.ID, err)
		}
		if branchRegistered(registrations, intent.BranchRef) {
			return Plan{}, fmt.Errorf("retained repository %s branch is checked out elsewhere", intent.ID)
		}
		plan.Repositories = append(plan.Repositories, RepositoryPlan{
			ID: intent.ID, SourcePath: intent.SourcePath, GitCommonDir: intent.GitCommonDir,
			BranchRef: intent.BranchRef, BranchOID: inactive.BranchOID, BranchOnly: true,
		})
	}
	sort.Slice(plan.Repositories, func(i, j int) bool { return plan.Repositories[i].ID < plan.Repositories[j].ID })
	return plan, nil
}

func branchRegistered(registrations []gitops.WorktreeRegistration, branchRef string) bool {
	for _, registration := range registrations {
		if registration.Branch == branchRef {
			return true
		}
	}
	return false
}

func (p Planner) git() Git {
	if p.Git != nil {
		return p.Git
	}
	return SystemGit{}
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

func inspectEntries(root string, repositories []inspectwork.RepositorySnapshot) ([]Entry, string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, "", fmt.Errorf("inspect Work root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("Work root is not a real directory: %s", root)
	}
	managed := make(map[string]struct{}, len(repositories))
	for _, repository := range repositories {
		managed[filepath.Clean(repository.Intent.Destination)] = struct{}{}
	}
	directory, err := os.ReadDir(root)
	if err != nil {
		return nil, "", fmt.Errorf("read Work root: %w", err)
	}
	fingerprint := sha256.New()
	entries := make([]Entry, 0, len(directory))
	for _, item := range directory {
		path := filepath.Join(root, item.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, "", err
		}
		entries = append(entries, Entry{Name: item.Name(), Kind: entryKind(info.Mode())})
		if _, isManaged := managed[filepath.Clean(path)]; isManaged {
			continue
		}
		if err := fingerprintEntry(fingerprint, root, path); err != nil {
			return nil, "", err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, hex.EncodeToString(fingerprint.Sum(nil)), nil
}

func entryKind(mode os.FileMode) string {
	switch {
	case mode.IsRegular():
		return "file"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	default:
		return "other"
	}
}

func fingerprintEntry(digest hash.Hash, root, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("fingerprint Work root entry %s: %w", path, err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve Work root entry %s: %w", path, err)
	}
	if filepath.Base(path) == ".git" {
		return fmt.Errorf("unmanaged nested Git repository %s blocks Remove Work", filepath.Dir(path))
	}
	writeFingerprintString(digest, filepath.ToSlash(relative))
	var mode [4]byte
	binary.BigEndian.PutUint32(mode[:], uint32(info.Mode()))
	_, _ = digest.Write(mode[:])

	switch {
	case info.Mode().IsRegular():
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open Work root entry %s: %w", path, err)
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return fmt.Errorf("inspect opened Work root entry %s: %w", path, statErr)
		}
		if !openedInfo.Mode().IsRegular() || openedInfo.Mode() != info.Mode() || openedInfo.Size() != info.Size() {
			_ = file.Close()
			return fmt.Errorf("Work root entry %s changed while fingerprinting", path)
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(info.Size()))
		_, _ = digest.Write(size[:])
		if _, err := io.Copy(digest, file); err != nil {
			_ = file.Close()
			return fmt.Errorf("read Work root entry %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close Work root entry %s: %w", path, err)
		}
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("read Work root symlink %s: %w", path, err)
		}
		writeFingerprintString(digest, target)
	case info.IsDir():
		children, err := os.ReadDir(path)
		if err != nil {
			return fmt.Errorf("read Work root directory %s: %w", path, err)
		}
		for _, child := range children {
			if err := fingerprintEntry(digest, root, filepath.Join(path, child.Name())); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsafe Work root entry type at %s: %s", path, info.Mode().Type())
	}
	return nil
}

func writeFingerprintString(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = io.WriteString(digest, value)
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
	ID       string    `json:"id"`
	Worktree StepState `json:"worktree,omitempty"`
	Branch   StepState `json:"branch,omitempty"`

	// Schema-v1 migration fields.
	WorktreeRemoved bool `json:"worktree_removed,omitempty"`
	BranchDeleted   bool `json:"branch_deleted,omitempty"`
}

type operationRecord struct {
	SchemaVersion int          `json:"schema_version"`
	RemovalID     string       `json:"removal_id,omitempty"`
	ArchiveID     string       `json:"archive_id,omitempty"`
	Kind          string       `json:"kind"`
	Phase         Phase        `json:"phase,omitempty"`
	Plan          Plan         `json:"plan"`
	Repositories  []checkpoint `json:"repositories"`
	Root          StepState    `json:"root,omitempty"`
	RootRemoved   bool         `json:"root_removed,omitempty"`
	CreatedAt     time.Time    `json:"created_at,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type ActiveRecord struct {
	WorkName string
	WorkID   string
	WorkRoot string
}

func LoadActive(path string) (ActiveRecord, error) {
	record, err := loadRecord(path)
	if err != nil {
		return ActiveRecord{}, err
	}
	if _, err := migrateAndValidate(record, record.Plan.WorkID); err != nil {
		return ActiveRecord{}, err
	}
	return ActiveRecord{WorkName: record.Plan.WorkName, WorkID: record.Plan.WorkID, WorkRoot: record.Plan.WorkRoot}, nil
}

type Archiver interface {
	NewID(string) (string, error)
	Publish(archiveops.PublishRequest) (archiveops.Summary, error)
}

type Safety interface {
	Check(context.Context, Plan) error
}

type SystemSafety struct{}

func (SystemSafety) Check(ctx context.Context, plan Plan) error {
	syncPath := syncWorkRecord(plan)
	if exists, terminal, err := syncwork.OperationTerminal(syncPath, plan.WorkID); err != nil {
		return fmt.Errorf("inspect Sync Work operation before removal: %w", err)
	} else if exists && !terminal {
		return fmt.Errorf("unfinished Sync Work operation blocks Remove Work; Resume Sync first")
	}
	prefix := "refs/goworktree/recovery/" + plan.WorkID + "/"
	for _, repository := range plan.Repositories {
		refs, err := gitops.ListRecoveryRefsContext(ctx, repository.SourcePath, prefix)
		if err != nil {
			return fmt.Errorf("repository %s: inspect private recovery refs: %w", repository.ID, err)
		}
		if len(refs) > 0 {
			return fmt.Errorf("repository %s has private Sync recovery ref %s; Resume Sync before Remove Work", repository.ID, refs[0].Ref)
		}
	}
	return nil
}

type Executor struct {
	Git          Git
	Locker       Locker
	Archiver     Archiver
	Safety       Safety
	Now          func() time.Time
	NewRemovalID func() (string, error)
	ReconcileFor time.Duration
	Fault        func(string) error
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
	if e.Safety == nil {
		return result, fmt.Errorf("Remove Work safety checker is nil")
	}
	if err := e.Safety.Check(ctx, plan); err != nil {
		return result, err
	}
	record, err := e.loadOrCreate(plan)
	if err != nil {
		return result, err
	}
	result.WorkName, result.ArchiveID = plan.WorkName, record.ArchiveID

	for index, repository := range record.Plan.Repositories {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		outcome := RepositoryResult{ID: repository.ID}
		if err := e.removeWorktree(ctx, &record, index); err != nil {
			outcome.Status, outcome.Err = "state-conflict", err
			result.Repositories = append(result.Repositories, outcome)
			return result, err
		}
		if err := ctx.Err(); err != nil {
			outcome.Status = "worktree-removed"
			result.Repositories = append(result.Repositories, outcome)
			return result, err
		}
		if err := e.removeBranch(ctx, &record, index); err != nil {
			outcome.Status, outcome.Err = "state-conflict", err
			result.Repositories = append(result.Repositories, outcome)
			return result, err
		}
		outcome.Status = "removed"
		result.Repositories = append(result.Repositories, outcome)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := e.removeRoot(&record); err != nil {
		return result, err
	}
	result.RootRemoved = true
	if err := ctx.Err(); err != nil {
		return result, err
	}
	record.Phase = PhaseArchiving
	if err := e.save(record); err != nil {
		return result, err
	}
	summary, err := e.archiver(plan).Publish(archiveops.PublishRequest{
		ArchiveID: record.ArchiveID, WorkName: plan.WorkName, WorkID: plan.WorkID, RemovalID: record.RemovalID,
		CreatedAt: e.now().UTC(), NewWorkRecord: newWorkRecord(plan), SyncWorkRecord: syncWorkRecord(plan),
		ChangeWorkRecord: changeWorkRecord(plan),
		RemoveWorkRecord: plan.OperationRecord,
	})
	if err != nil {
		return result, fmt.Errorf("publish Remove Work archive: %w", err)
	}
	result.ArchivePath = summary.Path
	if err := e.inject("archive-published"); err != nil {
		return result, err
	}
	if err := e.cleanupActiveRecords(plan); err != nil {
		return result, fmt.Errorf("finalize Remove Work records: %w", err)
	}
	return result, nil
}

type worktreeState int

const (
	worktreePresent worktreeState = iota
	worktreeRegisteredOnly
	worktreeAbsent
)

func (e Executor) inspectWorktree(ctx context.Context, plan RepositoryPlan) (worktreeState, error) {
	registrations, err := e.Git.ListWorktrees(ctx, plan.SourcePath)
	if err != nil {
		return worktreePresent, err
	}
	var registration *gitops.WorktreeRegistration
	for index := range registrations {
		if filepath.Clean(registrations[index].Path) == filepath.Clean(plan.Destination) {
			registration = &registrations[index]
			break
		}
	}
	pathInfo, pathErr := os.Lstat(plan.Destination)
	if errors.Is(pathErr, os.ErrNotExist) {
		if registration == nil {
			return worktreeAbsent, nil
		}
		if registration.Branch != plan.BranchRef || registration.HeadOID != plan.BranchOID {
			return worktreePresent, fmt.Errorf("repository %s worktree registration has a different identity", plan.ID)
		}
		return worktreeRegisteredOnly, nil
	}
	if pathErr != nil {
		return worktreePresent, pathErr
	}
	if !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return worktreePresent, fmt.Errorf("repository %s worktree path has a different identity", plan.ID)
	}
	if registration == nil || registration.Branch != plan.BranchRef || registration.HeadOID != plan.BranchOID {
		return worktreePresent, fmt.Errorf("repository %s worktree registration changed", plan.ID)
	}
	checkout, err := e.Git.InspectCheckout(ctx, plan.Destination)
	if err != nil {
		return worktreePresent, err
	}
	if checkout.Identity.CommonDir != plan.GitCommonDir || checkout.FullRef != plan.BranchRef || checkout.HeadOID != plan.BranchOID {
		return worktreePresent, fmt.Errorf("repository %s checkout identity, branch, or HEAD changed", plan.ID)
	}
	if plan.WorkingTreeFingerprint != "" {
		fingerprint, err := e.Git.WorktreeFingerprint(ctx, plan.Destination)
		if err != nil {
			return worktreePresent, fmt.Errorf("repository %s fingerprint working tree: %w", plan.ID, err)
		}
		if fingerprint != plan.WorkingTreeFingerprint {
			return worktreePresent, fmt.Errorf("repository %s working tree changed after confirmation", plan.ID)
		}
	}
	operations, err := e.Git.ActiveOperations(ctx, plan.Destination)
	if err != nil || len(operations) != 0 {
		return worktreePresent, fmt.Errorf("repository %s has an active Git operation", plan.ID)
	}
	return worktreePresent, nil
}

func (e Executor) removeWorktree(ctx context.Context, record *operationRecord, index int) error {
	checkpoint, plan := &record.Repositories[index], record.Plan.Repositories[index]
	if plan.BranchOnly {
		if checkpoint.Worktree == StepCompleted {
			return nil
		}
		checkpoint.Worktree = StepCompleted
		return e.save(*record)
	}
	if checkpoint.Worktree == StepCompleted {
		return nil
	}
	state, err := e.inspectWorktree(ctx, plan)
	if err != nil {
		return fmt.Errorf("inspect worktree before removal: %w", err)
	}
	if state == worktreeAbsent {
		checkpoint.Worktree = StepCompleted
		return e.save(*record)
	}
	if state == worktreePresent && plan.WorkingTreeFingerprint == "" {
		return legacyWorktreeAuthorizationError(record.Plan.OperationRecord, plan.ID)
	}
	if checkpoint.Worktree == StepPending {
		checkpoint.Worktree, record.Phase = StepIntent, PhaseWorktrees
		if err := e.save(*record); err != nil {
			return fmt.Errorf("checkpoint worktree removal intent: %w", err)
		}
		if err := e.inject("worktree-intent:" + plan.ID); err != nil {
			return err
		}
	}
	state, err = e.inspectWorktree(ctx, plan)
	if err != nil {
		return fmt.Errorf("revalidate worktree immediately before removal: %w", err)
	}
	if state == worktreeAbsent {
		checkpoint.Worktree = StepCompleted
		return e.save(*record)
	}
	if state == worktreePresent && plan.WorkingTreeFingerprint == "" {
		return legacyWorktreeAuthorizationError(record.Plan.OperationRecord, plan.ID)
	}
	mutationCtx, cancel := mutationContext(ctx)
	err = e.Git.RemoveWorktree(mutationCtx, plan.SourcePath, plan.Destination)
	cancel()
	if faultErr := e.inject("worktree-mutation:" + plan.ID); faultErr != nil {
		return faultErr
	}
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), e.reconcileDuration())
	defer reconcileCancel()
	state, reconcileErr := e.inspectWorktree(reconcileCtx, plan)
	if reconcileErr == nil && state == worktreeRegisteredOnly {
		pruneErr := e.Git.RemoveWorktree(reconcileCtx, plan.SourcePath, plan.Destination)
		state, reconcileErr = e.inspectWorktree(reconcileCtx, plan)
		if reconcileErr == nil && state != worktreeAbsent {
			reconcileErr = pruneErr
		}
	}
	if reconcileErr == nil && state == worktreeAbsent {
		checkpoint.Worktree = StepCompleted
		if saveErr := e.save(*record); saveErr != nil {
			return fmt.Errorf("checkpoint completed worktree removal: %w", saveErr)
		}
		if faultErr := e.inject("worktree-completed:" + plan.ID); faultErr != nil {
			return faultErr
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove repository %s worktree: %w", plan.ID, errors.Join(err, reconcileErr))
	}
	return fmt.Errorf("repository %s worktree removal could not be reconciled: %w", plan.ID, reconcileErr)
}

func (e Executor) removeBranch(ctx context.Context, record *operationRecord, index int) error {
	checkpoint, plan := &record.Repositories[index], record.Plan.Repositories[index]
	if checkpoint.Branch == StepCompleted {
		return nil
	}
	if plan.BranchOnly {
		identity, err := e.Git.InspectRepository(ctx, plan.SourcePath)
		if err != nil {
			return fmt.Errorf("inspect retained repository source: %w", err)
		}
		if identity.SourcePath != plan.SourcePath || identity.CommonDir != plan.GitCommonDir {
			return fmt.Errorf("retained repository %s source identity changed after confirmation", plan.ID)
		}
		registrations, err := e.Git.ListWorktrees(ctx, plan.SourcePath)
		if err != nil {
			return fmt.Errorf("inspect retained repository worktree registrations: %w", err)
		}
		if branchRegistered(registrations, plan.BranchRef) {
			return fmt.Errorf("retained repository %s branch is checked out elsewhere", plan.ID)
		}
	}
	oid, exists, err := e.Git.LocalBranchOID(ctx, plan.SourcePath, plan.BranchRef)
	if err != nil {
		return err
	}
	if !exists {
		checkpoint.Branch = StepCompleted
		return e.save(*record)
	}
	if oid != plan.BranchOID {
		return fmt.Errorf("branch %s points to a different commit after confirmation", plan.BranchRef)
	}
	if checkpoint.Branch == StepPending {
		checkpoint.Branch, record.Phase = StepIntent, PhaseBranches
		if err := e.save(*record); err != nil {
			return fmt.Errorf("checkpoint branch deletion intent: %w", err)
		}
		if err := e.inject("branch-intent:" + plan.ID); err != nil {
			return err
		}
	}
	mutationCtx, cancel := mutationContext(ctx)
	err = e.Git.DeleteLocalBranchAtOID(mutationCtx, plan.SourcePath, plan.BranchRef, plan.BranchOID)
	cancel()
	if faultErr := e.inject("branch-mutation:" + plan.ID); faultErr != nil {
		return faultErr
	}
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), e.reconcileDuration())
	defer reconcileCancel()
	oid, exists, reconcileErr := e.Git.LocalBranchOID(reconcileCtx, plan.SourcePath, plan.BranchRef)
	if reconcileErr == nil && !exists {
		checkpoint.Branch = StepCompleted
		if saveErr := e.save(*record); saveErr != nil {
			return fmt.Errorf("checkpoint completed branch deletion: %w", saveErr)
		}
		if faultErr := e.inject("branch-completed:" + plan.ID); faultErr != nil {
			return faultErr
		}
		return nil
	}
	if reconcileErr == nil && oid != plan.BranchOID {
		return fmt.Errorf("branch %s was replaced during deletion", plan.BranchRef)
	}
	if err != nil {
		return fmt.Errorf("delete local branch %s: %w", plan.BranchRef, errors.Join(err, reconcileErr))
	}
	return fmt.Errorf("branch %s deletion could not be reconciled: %w", plan.BranchRef, reconcileErr)
}

func (e Executor) removeRoot(record *operationRecord) error {
	if record.Root == StepCompleted {
		return nil
	}
	_, fingerprint, err := inspectEntries(record.Plan.WorkRoot, nil)
	if errors.Is(err, os.ErrNotExist) {
		record.Root = StepCompleted
		return e.save(*record)
	}
	if err != nil {
		return err
	}
	if record.Plan.RootFingerprint == "" {
		return legacyRootAuthorizationError(record.Plan.OperationRecord)
	}
	if fingerprint != record.Plan.RootFingerprint {
		return fmt.Errorf("Work root contents changed after confirmation")
	}
	if record.Root == StepPending {
		record.Root, record.Phase = StepIntent, PhaseRoot
		if err := e.save(*record); err != nil {
			return fmt.Errorf("checkpoint Work root removal intent: %w", err)
		}
		if err := e.inject("root-intent"); err != nil {
			return err
		}
	}
	_, fingerprint, err = inspectEntries(record.Plan.WorkRoot, nil)
	if errors.Is(err, os.ErrNotExist) {
		record.Root = StepCompleted
		return e.save(*record)
	}
	if err != nil {
		return err
	}
	if fingerprint != record.Plan.RootFingerprint {
		return fmt.Errorf("Work root contents changed immediately before removal")
	}
	if err := os.RemoveAll(record.Plan.WorkRoot); err != nil {
		return fmt.Errorf("remove Work root: %w", err)
	}
	if err := work.SyncDirectory(filepath.Dir(record.Plan.WorkRoot)); err != nil {
		return fmt.Errorf("sync Work root removal: %w", err)
	}
	if err := e.inject("root-mutation"); err != nil {
		return err
	}
	if _, err := os.Lstat(record.Plan.WorkRoot); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Work root still exists after removal")
	}
	record.Root = StepCompleted
	if err := e.save(*record); err != nil {
		return err
	}
	return e.inject("root-completed")
}

func legacyWorktreeAuthorizationError(operationRecord, repositoryID string) error {
	return fmt.Errorf("legacy Remove Work operation lacks an exact working tree fingerprint for repository %s; inspect and manually remove that worktree, then Resume Remove Work (record: %s)", repositoryID, operationRecord)
}

func legacyRootAuthorizationError(operationRecord string) error {
	return fmt.Errorf("legacy Remove Work operation lacks an exact Work root fingerprint; inspect and manually remove the Work root, then Resume Remove Work (record: %s)", operationRecord)
}

func (e Executor) loadOrCreate(plan Plan) (operationRecord, error) {
	record, err := loadRecord(plan.OperationRecord)
	if err == nil {
		migrated, err := migrateAndValidate(record, plan.WorkID)
		if err != nil {
			return record, err
		}
		record = migrated
		if record.Plan.WorkName != plan.WorkName {
			return record, fmt.Errorf("Remove Work operation record does not match this Work")
		}
		if record.RemovalID == "" {
			record.RemovalID, err = e.removalID()
			if err != nil {
				return record, err
			}
		}
		if record.ArchiveID == "" {
			record.ArchiveID, err = e.archiver(plan).NewID(plan.WorkName)
			if err != nil {
				return record, err
			}
		}
		if err := e.save(record); err != nil {
			return record, fmt.Errorf("persist migrated Remove Work operation: %w", err)
		}
		return record, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return record, err
	}
	removalID, err := e.removalID()
	if err != nil {
		return record, err
	}
	archiveID, err := e.archiver(plan).NewID(plan.WorkName)
	if err != nil {
		return record, err
	}
	record = operationRecord{
		SchemaVersion: operationSchemaVersion, RemovalID: removalID, ArchiveID: archiveID,
		Kind: "remove-work", Phase: PhaseWorktrees, Plan: plan, Root: StepPending,
		CreatedAt: e.now().UTC(), UpdatedAt: e.now().UTC(),
	}
	for _, repository := range plan.Repositories {
		record.Repositories = append(record.Repositories, checkpoint{ID: repository.ID, Worktree: StepPending, Branch: StepPending})
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		return record, err
	}
	if err := work.CreateJSON(plan.OperationRecord, record, 0o600); err != nil {
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
		return operationRecord{}, fmt.Errorf("Remove Work operation record path does not match its plan")
	}
	return record, nil
}

func migrateAndValidate(record operationRecord, workID string) (operationRecord, error) {
	originalSchema := record.SchemaVersion
	if record.Kind != "remove-work" || record.Plan.WorkID != workID {
		return record, fmt.Errorf("Remove Work operation record is invalid")
	}
	if len(record.Repositories) != len(record.Plan.Repositories) {
		return record, fmt.Errorf("Remove Work operation checkpoints do not match plan")
	}
	if record.SchemaVersion == 1 {
		record.SchemaVersion = operationSchemaVersion
		record.Phase = PhaseWorktrees
		if record.CreatedAt.IsZero() {
			record.CreatedAt = record.Plan.BuiltAt
		}
		for index := range record.Repositories {
			if record.Repositories[index].WorktreeRemoved {
				record.Repositories[index].Worktree = StepCompleted
			} else {
				record.Repositories[index].Worktree = StepPending
			}
			if record.Repositories[index].BranchDeleted {
				record.Repositories[index].Branch = StepCompleted
			} else {
				record.Repositories[index].Branch = StepPending
			}
			record.Repositories[index].WorktreeRemoved = false
			record.Repositories[index].BranchDeleted = false
		}
		if record.RootRemoved {
			record.Root = StepCompleted
		} else {
			record.Root = StepPending
		}
		record.RootRemoved = false
		record.Phase = inferredPhase(record)
	}
	if record.SchemaVersion == 2 {
		record.SchemaVersion = operationSchemaVersion
	}
	if record.SchemaVersion != operationSchemaVersion {
		return record, fmt.Errorf("unsupported Remove Work operation schema %d", record.SchemaVersion)
	}
	if _, err := work.ParseName(record.Plan.WorkName); err != nil || record.Plan.WorkRoot == "" || record.Plan.OperationRecord == "" {
		return record, fmt.Errorf("Remove Work operation plan is incomplete")
	}
	switch record.Plan.FingerprintVersion {
	case 0:
		// Schemas 1 and 2 did not record exact destructive authorization.
		// Runtime reconciliation may finish steps whose target is already gone,
		// but it must not mutate a still-present target.
	case fingerprintVersion:
		if record.Plan.RootFingerprint == "" {
			return record, fmt.Errorf("Remove Work operation plan has no Work root fingerprint")
		}
		for _, repository := range record.Plan.Repositories {
			if !repository.BranchOnly && repository.WorkingTreeFingerprint == "" {
				return record, fmt.Errorf("Remove Work operation plan has no working tree fingerprint for repository %s", repository.ID)
			}
		}
	default:
		return record, fmt.Errorf("unsupported Remove Work fingerprint version %d", record.Plan.FingerprintVersion)
	}
	if originalSchema != 1 {
		if len(record.RemovalID) != 32 {
			return record, fmt.Errorf("Remove Work removal ID is invalid")
		}
		if _, err := hex.DecodeString(record.RemovalID); err != nil {
			return record, fmt.Errorf("Remove Work removal ID is invalid")
		}
		if err := archiveops.ValidateID(record.ArchiveID); err != nil {
			return record, fmt.Errorf("Remove Work archive ID is invalid: %w", err)
		}
	}
	switch record.Phase {
	case PhaseWorktrees, PhaseBranches, PhaseRoot, PhaseArchiving:
	default:
		return record, fmt.Errorf("Remove Work operation phase is invalid")
	}
	for index := range record.Repositories {
		if record.Repositories[index].ID != record.Plan.Repositories[index].ID {
			return record, fmt.Errorf("Remove Work operation checkpoint %d does not match plan", index)
		}
		if !validStep(record.Repositories[index].Worktree) || !validStep(record.Repositories[index].Branch) {
			return record, fmt.Errorf("Remove Work operation checkpoint %d has an invalid state", index)
		}
		if record.Repositories[index].Branch != StepPending && record.Repositories[index].Worktree != StepCompleted {
			return record, fmt.Errorf("Remove Work branch checkpoint %d precedes worktree completion", index)
		}
	}
	if !validStep(record.Root) {
		return record, fmt.Errorf("Remove Work root checkpoint has an invalid state")
	}
	if record.Root != StepPending {
		for index := range record.Repositories {
			if record.Repositories[index].Branch != StepCompleted {
				return record, fmt.Errorf("Remove Work root checkpoint precedes branch completion")
			}
		}
	}
	if record.Phase == PhaseArchiving && record.Root != StepCompleted {
		return record, fmt.Errorf("Remove Work archive phase precedes root completion")
	}
	return record, nil
}

func inferredPhase(record operationRecord) Phase {
	if record.Root == StepCompleted {
		return PhaseArchiving
	}
	for index, checkpoint := range record.Repositories {
		if !record.Plan.Repositories[index].BranchOnly && checkpoint.Worktree != StepCompleted {
			return PhaseWorktrees
		}
	}
	for _, checkpoint := range record.Repositories {
		if checkpoint.Branch != StepCompleted {
			return PhaseBranches
		}
	}
	return PhaseRoot
}

func validStep(state StepState) bool {
	return state == StepPending || state == StepIntent || state == StepCompleted
}

func (e Executor) save(record operationRecord) error {
	record.UpdatedAt = e.now().UTC()
	if _, err := migrateAndValidate(record, record.Plan.WorkID); err != nil {
		return err
	}
	return work.ReplaceJSON(record.Plan.OperationRecord, record, 0o600, nil)
}

func (e Executor) archiver(plan Plan) Archiver {
	if e.Archiver != nil {
		return e.Archiver
	}
	return archiveops.Store{ControlRoot: controlRoot(plan)}
}

func (e Executor) removalID() (string, error) {
	if e.NewRemovalID != nil {
		return e.NewRemovalID()
	}
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate removal ID: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func (e Executor) reconcileDuration() time.Duration {
	if e.ReconcileFor > 0 {
		return e.ReconcileFor
	}
	return 30 * time.Second
}

func (e Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func mutationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.WithCancel(context.Background())
}

func controlRoot(plan Plan) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Clean(plan.OperationRecord))))
}

func newWorkRecord(plan Plan) string {
	return filepath.Join(controlRoot(plan), "operations", "new-work", plan.WorkID+".json")
}

func syncWorkRecord(plan Plan) string {
	return filepath.Join(controlRoot(plan), "operations", "sync-work", plan.WorkID+".json")
}

func changeWorkRecord(plan Plan) string {
	return filepath.Join(controlRoot(plan), "operations", "change-work", plan.WorkID+".json")
}

func (e Executor) cleanupActiveRecords(plan Plan) error {
	records := []struct {
		path, point string
	}{
		{newWorkRecord(plan), "active-new-work-deleted"},
		{syncWorkRecord(plan), "active-sync-work-deleted"},
		{changeWorkRecord(plan), "active-change-work-deleted"},
		{plan.OperationRecord, "active-remove-work-deleted"},
	}
	for _, record := range records {
		path := record.path
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("active operation record is not a regular file: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := work.SyncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		if err := e.inject(record.point); err != nil {
			return err
		}
	}
	return nil
}

func (e Executor) inject(point string) error {
	if e.Fault == nil {
		return nil
	}
	if err := e.Fault(point); err != nil {
		return fmt.Errorf("injected fault at %s: %w", point, err)
	}
	return nil
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
