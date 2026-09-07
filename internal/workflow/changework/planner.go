package changework

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

type PlanningGit interface {
	InspectRepository(context.Context, string) (gitops.RepositoryIdentity, error)
	FetchRemote(context.Context, string, string) error
	ResolveBase(context.Context, string, string, string) (gitops.ResolvedBase, error)
	LocalBranchOID(context.Context, string, string) (string, bool, error)
	ListWorktrees(context.Context, string) ([]gitops.WorktreeRegistration, error)
	RootFileAtCommit(context.Context, string, string, string) (bool, error)
	InspectCheckout(context.Context, string) (gitops.Checkout, error)
	WorkingTreeStatus(context.Context, string) (gitops.WorkingTreeStatus, error)
	ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error)
	WorktreeFingerprint(context.Context, string) (string, error)
}

type RepositoryLocker interface {
	AcquireRepositories(context.Context, []string) (func() error, error)
}

type Planner struct {
	Git    PlanningGit
	Locker RepositoryLocker
	Now    func() time.Time
}

type SystemGit struct{}

func (SystemGit) InspectRepository(ctx context.Context, path string) (gitops.RepositoryIdentity, error) {
	return gitops.InspectRepositoryContext(ctx, path)
}
func (SystemGit) FetchRemote(ctx context.Context, repo, remote string) error {
	return gitops.FetchRemoteContext(ctx, repo, remote)
}
func (SystemGit) ResolveBase(ctx context.Context, repo, remote, preference string) (gitops.ResolvedBase, error) {
	return gitops.ResolveNewWorkBaseContext(ctx, repo, remote, "", preference)
}
func (SystemGit) LocalBranchOID(ctx context.Context, repo, branch string) (string, bool, error) {
	return gitops.LocalBranchOIDContext(ctx, repo, branch)
}
func (SystemGit) ListWorktrees(ctx context.Context, repo string) ([]gitops.WorktreeRegistration, error) {
	return gitops.ListWorktreesContext(ctx, repo)
}
func (SystemGit) RootFileAtCommit(ctx context.Context, repo, oid, name string) (bool, error) {
	return gitops.RootFileAtCommitContext(ctx, repo, oid, name)
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
func (SystemGit) WorktreeFingerprint(ctx context.Context, path string) (string, error) {
	return gitops.WorktreeFingerprintContext(ctx, path)
}

func (p Planner) BuildAdd(ctx context.Context, catalog Catalog, request AddRequest) (Plan, error) {
	if p.Git == nil {
		return Plan{}, fmt.Errorf("Change Work Git adapter is nil")
	}
	mode := request.Mode
	if mode == "" {
		mode = newwork.ModeOnline
	}
	if mode != newwork.ModeOnline && mode != newwork.ModeOffline {
		return Plan{}, fmt.Errorf("unsupported add mode %q", mode)
	}
	plan, err := p.basePlan(catalog, request.WorkName, request.RepositoryIDs, KindAdd)
	if err != nil {
		return Plan{}, err
	}
	plan.Mode = mode

	inactive := inactiveByID(plan.Before)
	active := activeByID(plan.Before)
	identities := make([]string, 0, len(request.RepositoryIDs))
	type prepared struct {
		config   Repository
		identity gitops.RepositoryIdentity
		old      *work.InactiveRepositoryIntent
	}
	preparedRepos := make([]prepared, 0, len(request.RepositoryIDs))
	selectedIdentities := make(map[string]string, len(request.RepositoryIDs))
	for _, id := range uniqueSorted(request.RepositoryIDs) {
		if _, exists := active[id]; exists {
			return Plan{}, fmt.Errorf("repository %q is already active in Work %q", id, request.WorkName)
		}
		definition, exists := catalog.Repositories[id]
		if !exists {
			return Plan{}, fmt.Errorf("repository %q is not configured", id)
		}
		identity, err := p.Git.InspectRepository(ctx, definition.SourcePath)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: inspect source: %w", id, err)
		}
		var old *work.InactiveRepositoryIntent
		if value, exists := inactive[id]; exists {
			copy := value
			old = &copy
			if value.Repository.GitCommonDir != identity.CommonDir || value.Repository.SourcePath != identity.SourcePath {
				return Plan{}, fmt.Errorf("repository %s: configured source no longer matches retained Work identity", id)
			}
		}
		if err := rejectDuplicateIdentity(plan.Before, id, identity.CommonDir); err != nil {
			return Plan{}, err
		}
		if first, exists := selectedIdentities[identity.CommonDir]; exists {
			return Plan{}, fmt.Errorf("repositories %s and %s resolve to the same Git identity", first, id)
		}
		selectedIdentities[identity.CommonDir] = id
		identities = append(identities, identity.CommonDir)
		preparedRepos = append(preparedRepos, prepared{config: definition, identity: identity, old: old})
	}

	var release func() error
	if mode == newwork.ModeOnline {
		if p.Locker == nil {
			return Plan{}, fmt.Errorf("Change Work repository locker is nil")
		}
		release, err = p.Locker.AcquireRepositories(ctx, identities)
		if err != nil {
			return Plan{}, fmt.Errorf("lock repositories: %w", err)
		}
		defer func() { _ = release() }()
	}

	for _, prepared := range preparedRepos {
		entry, intent, err := p.planAddRepository(ctx, plan, prepared.config, prepared.identity, prepared.old, mode)
		if err != nil {
			return Plan{}, err
		}
		plan.Repositories = append(plan.Repositories, entry)
		plan.After.Repositories = append(plan.After.Repositories, intent)
		removeInactive(&plan.After, intent.ID)
		addHarnessRepository(&plan.After, intent)
	}
	stableManifest(&plan.After)
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (p Planner) BuildRemove(ctx context.Context, catalog Catalog, request RemoveRequest) (Plan, error) {
	if p.Git == nil {
		return Plan{}, fmt.Errorf("Change Work Git adapter is nil")
	}
	plan, err := p.basePlan(catalog, request.WorkName, request.RepositoryIDs, KindRemove)
	if err != nil {
		return Plan{}, err
	}
	selected := uniqueSorted(request.RepositoryIDs)
	if len(plan.Before.Repositories)-len(selected) < 1 {
		return Plan{}, fmt.Errorf("cannot remove the last repository; use Remove Work instead")
	}
	active := activeByID(plan.Before)
	for _, id := range selected {
		intent, exists := active[id]
		if !exists {
			return Plan{}, fmt.Errorf("repository %q is not active in Work %q", id, request.WorkName)
		}
		checkout, err := p.Git.InspectCheckout(ctx, intent.Destination)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: inspect checkout: %w", id, err)
		}
		if checkout.Identity.CommonDir != intent.GitCommonDir || checkout.FullRef != intent.BranchRef || checkout.Detached {
			return Plan{}, fmt.Errorf("repository %s: checkout identity or branch does not match Work intent", id)
		}
		status, err := p.Git.WorkingTreeStatus(ctx, intent.Destination)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: inspect working tree: %w", id, err)
		}
		if status.Dirty() {
			return Plan{}, fmt.Errorf("repository %s has local changes; clean or commit them before removing it", id)
		}
		operations, err := p.Git.ActiveOperations(ctx, intent.Destination)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: inspect active Git operations: %w", id, err)
		}
		if len(operations) > 0 {
			return Plan{}, fmt.Errorf("repository %s has active Git operation %s", id, operations[0])
		}
		branchOID, exists, err := p.Git.LocalBranchOID(ctx, intent.SourcePath, intent.BranchRef)
		if err != nil || !exists || branchOID != checkout.HeadOID {
			return Plan{}, fmt.Errorf("repository %s: local Work branch cannot be proven at checkout HEAD", id)
		}
		fingerprint, err := p.Git.WorktreeFingerprint(ctx, intent.Destination)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: fingerprint worktree: %w", id, err)
		}
		registrations, err := p.Git.ListWorktrees(ctx, intent.SourcePath)
		if err != nil {
			return Plan{}, fmt.Errorf("repository %s: inspect worktree registrations: %w", id, err)
		}
		matchingDestination, matchingBranch, matchingExact := 0, 0, 0
		for _, registration := range registrations {
			destinationMatch := samePath(registration.Path, intent.Destination)
			branchMatch := registration.Branch == intent.BranchRef
			if destinationMatch {
				matchingDestination++
			}
			if branchMatch {
				matchingBranch++
			}
			if destinationMatch && branchMatch && !registration.Detached && registration.HeadOID == branchOID {
				matchingExact++
			}
		}
		if matchingDestination != 1 || matchingBranch != 1 || matchingExact != 1 {
			return Plan{}, fmt.Errorf("repository %s: Work branch registration is ambiguous", id)
		}
		plan.Repositories = append(plan.Repositories, RepositoryPlan{
			ID: id, SourcePath: intent.SourcePath, GitCommonDir: intent.GitCommonDir,
			BaseRef: intent.BaseRef, BaseOID: intent.BaseOID, BranchRef: intent.BranchRef,
			BranchOID: branchOID, Destination: intent.Destination, IncludeInGoWork: intent.IncludeInGoWork,
			DeleteBranch: request.DeleteBranches, WorkingTree: status, Fingerprint: fingerprint,
		})
		removeActive(&plan.After, id)
		removeInactive(&plan.After, id)
		plan.After.InactiveRepositories = append(plan.After.InactiveRepositories, work.InactiveRepositoryIntent{
			Repository:     intent,
			BranchRetained: !request.DeleteBranches,
			BranchOID:      retainedBranchOID(request.DeleteBranches, branchOID),
		})
		removeHarnessRepository(&plan.After, intent)
	}
	stableManifest(&plan.After)
	if err := plan.Validate(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (p Planner) basePlan(catalog Catalog, requestName string, repositoryIDs []string, kind Kind) (Plan, error) {
	if len(repositoryIDs) == 0 {
		return Plan{}, fmt.Errorf("select at least one repository")
	}
	if err := catalog.Manifest.Validate(); err != nil {
		return Plan{}, fmt.Errorf("Work manifest is invalid: %w", err)
	}
	name, err := work.ParseName(requestName)
	if err != nil || name != catalog.Manifest.Name {
		return Plan{}, fmt.Errorf("Work name does not match manifest")
	}
	root, err := filepath.Abs(catalog.WorkRoot)
	if err != nil || filepath.Clean(root) != filepath.Clean(catalog.WorkRoot) {
		return Plan{}, fmt.Errorf("Work root must be absolute")
	}
	if work.NewIdentity(filepath.Dir(root), name) != catalog.Manifest.WorkID {
		return Plan{}, fmt.Errorf("Work identity does not match its root")
	}
	operationID, err := randomID()
	if err != nil {
		return Plan{}, err
	}
	before := cloneManifest(catalog.Manifest)
	after := cloneManifest(catalog.Manifest)
	after.SchemaVersion = work.ManifestSchemaVersion
	after.Revision++
	after.LastChangeID = operationID
	operationPath := filepath.Join(catalog.ControlRoot, "operations", "change-work", catalog.Manifest.WorkID.String()+".json")
	if record, err := LoadRecord(operationPath); err == nil {
		if record.Phase != PhaseCompleted {
			return Plan{}, fmt.Errorf("unfinished repository change exists; Resume it before planning another change")
		}
		if catalog.Manifest.Revision == 0 || record.OperationID != catalog.Manifest.LastChangeID || !RecordMatchesManifest(record, catalog.Manifest) {
			return Plan{}, fmt.Errorf("completed Change Work operation does not match the current manifest")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Plan{}, fmt.Errorf("inspect Change Work operation: %w", err)
	}
	harnessBefore, harnessBeforeExists, err := RenderManifestHarness(before, root)
	if err != nil {
		return Plan{}, fmt.Errorf("render current go.work intent: %w", err)
	}
	currentHarness, readErr := work.ReadRegularFile(filepath.Join(root, "go.work"))
	currentHarnessExists := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Plan{}, fmt.Errorf("read current go.work: %w", readErr)
	}
	if currentHarnessExists != harnessBeforeExists || !bytes.Equal(currentHarness, harnessBefore) {
		return Plan{}, fmt.Errorf("current go.work does not match the Work manifest")
	}
	return Plan{
		OperationID: operationID, OperationPath: operationPath, Kind: kind,
		WorkName: name, WorkID: catalog.Manifest.WorkID, WorkRoot: root,
		Before: before, After: after, NoRemoteMutation: true,
		HarnessBefore: harnessBefore, HarnessBeforeExists: harnessBeforeExists,
	}, nil
}

func (p Planner) planAddRepository(ctx context.Context, plan Plan, definition Repository, identity gitops.RepositoryIdentity, old *work.InactiveRepositoryIntent, mode newwork.Mode) (RepositoryPlan, work.RepositoryIntent, error) {
	branchRef := "refs/heads/" + plan.WorkName.String()
	destination := filepath.Join(plan.WorkRoot, definition.Folder)
	if old != nil {
		destination = old.Repository.Destination
		branchRef = old.Repository.BranchRef
	}
	if filepath.Dir(filepath.Clean(destination)) != filepath.Clean(plan.WorkRoot) {
		return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s destination is outside Work root", definition.ID)
	}
	if _, err := os.Lstat(destination); err == nil {
		return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s destination already exists: %s", definition.ID, destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return RepositoryPlan{}, work.RepositoryIntent{}, err
	}
	registrations, err := p.Git.ListWorktrees(ctx, identity.SourcePath)
	if err != nil {
		return RepositoryPlan{}, work.RepositoryIntent{}, err
	}
	for _, registration := range registrations {
		if registration.Branch == branchRef || samePath(registration.Path, destination) {
			return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s branch or destination is already registered at %s", definition.ID, registration.Path)
		}
	}
	branchOID, branchExists, err := p.Git.LocalBranchOID(ctx, identity.SourcePath, branchRef)
	if err != nil {
		return RepositoryPlan{}, work.RepositoryIntent{}, err
	}
	if branchExists {
		if old == nil || !old.BranchRetained || old.Repository.BranchRef != branchRef || old.BranchOID != branchOID {
			return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s has an unowned Work branch %s", definition.ID, branchRef)
		}
		intent := old.Repository
		entry := RepositoryPlan{
			ID: definition.ID, SourcePath: identity.SourcePath, GitCommonDir: identity.CommonDir,
			BaseRef: intent.BaseRef, BaseOID: intent.BaseOID, BranchRef: branchRef, BranchOID: branchOID,
			Destination: destination, IncludeInGoWork: intent.IncludeInGoWork, Reattach: true,
		}
		return entry, intent, nil
	}
	if old != nil && old.BranchRetained {
		return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s retained Work branch %s is missing", definition.ID, branchRef)
	}
	if mode == newwork.ModeOnline {
		if err := p.Git.FetchRemote(ctx, identity.SourcePath, definition.Remote); err != nil {
			return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s: fetch %s: %w", definition.ID, definition.Remote, err)
		}
	}
	base, err := p.Git.ResolveBase(ctx, identity.SourcePath, definition.Remote, definition.BasePreference)
	if err != nil {
		return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s: resolve base: %w", definition.ID, err)
	}
	rootModule, err := p.Git.RootFileAtCommit(ctx, identity.SourcePath, base.OID, "go.mod")
	if err != nil {
		return RepositoryPlan{}, work.RepositoryIntent{}, fmt.Errorf("repository %s: inspect root module: %w", definition.ID, err)
	}
	intent := work.RepositoryIntent{
		ID: definition.ID, SourcePath: identity.SourcePath, GitCommonDir: identity.CommonDir,
		BaseRef: base.FullRef, BaseOID: base.OID, BranchRef: branchRef,
		Destination: destination, IncludeInGoWork: rootModule,
	}
	entry := RepositoryPlan{
		ID: definition.ID, SourcePath: identity.SourcePath, GitCommonDir: identity.CommonDir,
		BaseRef: base.FullRef, BaseOID: base.OID, BranchRef: branchRef, BranchOID: base.OID,
		Destination: destination, IncludeInGoWork: rootModule,
	}
	if mode == newwork.ModeOnline {
		now := p.now().UTC()
		entry.FetchedAt = &now
	}
	return entry, intent, nil
}

func cloneManifest(value work.Manifest) work.Manifest {
	value.Repositories = append([]work.RepositoryIntent(nil), value.Repositories...)
	value.InactiveRepositories = append([]work.InactiveRepositoryIntent(nil), value.InactiveRepositories...)
	value.Harness.RepositoryIDs = append([]string(nil), value.Harness.RepositoryIDs...)
	value.Harness.UsePaths = append([]string(nil), value.Harness.UsePaths...)
	return value
}

func activeByID(manifest work.Manifest) map[string]work.RepositoryIntent {
	result := make(map[string]work.RepositoryIntent, len(manifest.Repositories))
	for _, repository := range manifest.Repositories {
		result[repository.ID] = repository
	}
	return result
}

func inactiveByID(manifest work.Manifest) map[string]work.InactiveRepositoryIntent {
	result := make(map[string]work.InactiveRepositoryIntent, len(manifest.InactiveRepositories))
	for _, repository := range manifest.InactiveRepositories {
		result[repository.Repository.ID] = repository
	}
	return result
}

func rejectDuplicateIdentity(manifest work.Manifest, id, commonDir string) error {
	for _, repository := range manifest.Repositories {
		if repository.ID != id && repository.GitCommonDir == commonDir {
			return fmt.Errorf("repository %s duplicates active repository %s", id, repository.ID)
		}
	}
	for _, inactive := range manifest.InactiveRepositories {
		if inactive.Repository.ID != id && inactive.Repository.GitCommonDir == commonDir {
			return fmt.Errorf("repository %s duplicates retained repository %s", id, inactive.Repository.ID)
		}
	}
	return nil
}

func uniqueSorted(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func removeActive(manifest *work.Manifest, id string) {
	result := manifest.Repositories[:0]
	for _, repository := range manifest.Repositories {
		if repository.ID != id {
			result = append(result, repository)
		}
	}
	manifest.Repositories = result
}

func removeInactive(manifest *work.Manifest, id string) {
	result := manifest.InactiveRepositories[:0]
	for _, repository := range manifest.InactiveRepositories {
		if repository.Repository.ID != id {
			result = append(result, repository)
		}
	}
	manifest.InactiveRepositories = result
}

func addHarnessRepository(manifest *work.Manifest, repository work.RepositoryIntent) {
	if !manifest.Harness.RootModulesOnly && repository.IncludeInGoWork {
		path := "./" + filepath.ToSlash(filepath.Base(repository.Destination))
		if !contains(manifest.Harness.UsePaths, path) {
			manifest.Harness.UsePaths = append(manifest.Harness.UsePaths, path)
		}
	}
}

func removeHarnessRepository(manifest *work.Manifest, repository work.RepositoryIntent) {
	if manifest.Harness.RootModulesOnly {
		return
	}
	prefix := "./" + filepath.ToSlash(filepath.Base(repository.Destination))
	paths := manifest.Harness.UsePaths[:0]
	for _, path := range manifest.Harness.UsePaths {
		if path != prefix && !strings.HasPrefix(path, prefix+"/") {
			paths = append(paths, path)
		}
	}
	manifest.Harness.UsePaths = paths
}

func retainedBranchOID(deleteBranch bool, oid string) string {
	if deleteBranch {
		return ""
	}
	return oid
}

func stableManifest(manifest *work.Manifest) {
	sort.Slice(manifest.Repositories, func(i, j int) bool { return manifest.Repositories[i].ID < manifest.Repositories[j].ID })
	sort.Slice(manifest.InactiveRepositories, func(i, j int) bool {
		return manifest.InactiveRepositories[i].Repository.ID < manifest.InactiveRepositories[j].Repository.ID
	})
	manifest.Harness.RepositoryIDs = manifest.Harness.RepositoryIDs[:0]
	for _, repository := range manifest.Repositories {
		if repository.IncludeInGoWork {
			manifest.Harness.RepositoryIDs = append(manifest.Harness.RepositoryIDs, repository.ID)
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func samePath(left, right string) bool {
	leftCanonical, leftErr := comparablePath(left)
	rightCanonical, rightErr := comparablePath(right)
	return leftErr == nil && rightErr == nil && leftCanonical == rightCanonical
}

func comparablePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return filepath.Clean(abs), nil
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

// RenderManifestHarness renders the exact go.work owned by one manifest.
func RenderManifestHarness(manifest work.Manifest, root string) ([]byte, bool, error) {
	plan := newwork.Plan{
		WorkName: manifest.Name, WorkID: manifest.WorkID, WorkRoot: root,
		Mode: newwork.ModeOffline, NoRemoteMutation: true,
		HarnessExplicit: !manifest.Harness.RootModulesOnly,
	}
	if !manifest.Harness.RootModulesOnly {
		plan.HarnessUsePaths = append([]string(nil), manifest.Harness.UsePaths...)
	}
	for _, repository := range manifest.Repositories {
		plan.Repositories = append(plan.Repositories, newwork.RepositoryPlan{
			ID: repository.ID, SourcePath: repository.SourcePath, GitCommonDir: repository.GitCommonDir,
			BaseRef: repository.BaseRef, BaseOID: repository.BaseOID, TargetBranchRef: repository.BranchRef,
			Destination: repository.Destination, IncludeInGoWork: repository.IncludeInGoWork,
		})
	}
	return newwork.RenderGoWork(plan)
}

func (p Planner) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
