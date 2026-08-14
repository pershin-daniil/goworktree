package syncwork

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
)

func TestPlannerFetchesAndRecordsExactBaseOID(t *testing.T) {
	git := &fakeGit{base: gitops.ResolvedBase{FullRef: "refs/remotes/upstream/main", OID: "base"}}
	snapshot := healthySnapshot(t, "b", "head")
	plan, err := (Planner{Git: git, Now: func() time.Time { return time.Unix(42, 0) }}).Build(
		context.Background(), snapshot, t.TempDir(), []RepositoryConfig{{ID: "b", Remote: "upstream", BasePreference: "main"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repos) != 1 || plan.Repos[0].BaseOID != "base" || plan.Repos[0].Relation != RelationDiverged || plan.Repos[0].InitialFingerprint != "clean" {
		t.Fatalf("plan = %+v", plan)
	}
	if git.fetched != "source:upstream" {
		t.Fatalf("fetch = %q", git.fetched)
	}
}

func TestPlannerBlocksOrphanRecoveryRefBeforeFetch(t *testing.T) {
	snapshot := healthySnapshot(t, "repo", "head")
	prefix := "refs/goworktree/recovery/" + snapshot.WorkID.String() + "/"
	git := &fakeGit{recovery: map[string]string{prefix + "old/repo": "oid"}}
	_, err := (Planner{Git: git}).Build(context.Background(), snapshot, t.TempDir(), []RepositoryConfig{{ID: "repo", Remote: "origin"}})
	if err == nil || !strings.Contains(err.Error(), "without an unfinished Sync record") {
		t.Fatalf("error = %v", err)
	}
	if git.fetched != "" {
		t.Fatalf("fetch ran before orphan recovery was blocked: %q", git.fetched)
	}
}

func TestExecutorContinuesAfterRepositoryFailure(t *testing.T) {
	git := &fakeGit{checkouts: map[string]gitops.Checkout{
		"a": {Identity: gitops.RepositoryIdentity{CommonDir: "common-a"}, FullRef: "refs/heads/work", HeadOID: "head-a"},
		"b": {Identity: gitops.RepositoryIdentity{CommonDir: "common-b"}, FullRef: "refs/heads/work", HeadOID: "head-b"},
	}, rebase: map[string]gitops.SyncResult{
		"a": {Status: gitops.SyncConflict, Err: errors.New("conflict")},
		"b": {Status: gitops.SyncRebased, From: "head-b", To: "new-b"},
	}}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: t.TempDir() + "/sync.json", Repos: []RepositoryPlan{
		{ID: "a", Destination: "a", GitCommonDir: "common-a", BranchRef: "refs/heads/work", PreHeadOID: "head-a", BaseOID: "base-a", Relation: RelationDiverged, InitialFingerprint: "clean"},
		{ID: "b", Destination: "b", GitCommonDir: "common-b", BranchRef: "refs/heads/work", PreHeadOID: "head-b", BaseOID: "base-b", Relation: RelationDiverged, InitialFingerprint: "clean"},
	}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 2 || result.Repositories[0].Status != gitops.SyncConflict || result.Repositories[1].Status != gitops.SyncRebased {
		t.Fatalf("result = %+v", result)
	}
	if len(git.rebased) != 2 || git.rebased[1] != "b:base-b" {
		t.Fatalf("rebases = %v", git.rebased)
	}
}

func TestExecutorRejectsChangedHeadWithoutMutation(t *testing.T) {
	git := &fakeGit{checkouts: map[string]gitops.Checkout{
		"repo": {Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "changed"},
	}}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: t.TempDir() + "/sync.json", Repos: []RepositoryPlan{{
		ID: "repo", Destination: "repo", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "planned", BaseOID: "base", Relation: RelationDiverged, InitialFingerprint: "clean",
	}}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(git.rebased) != 0 || result.Repositories[0].Err == nil {
		t.Fatalf("result = %+v; rebases = %v", result, git.rebased)
	}
}

func TestExecutorRejectsSameCountWorkingTreeContentChange(t *testing.T) {
	dirty := gitops.WorkingTreeStatus{Unstaged: 1}
	git := &fakeGit{
		checkouts: map[string]gitops.Checkout{"repo": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "head",
		}},
		statuses:     map[string]gitops.WorkingTreeStatus{"repo": dirty},
		fingerprints: map[string]string{"repo": "changed-content"},
	}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: filepath.Join(t.TempDir(), "sync.json"), Repos: []RepositoryPlan{{
		ID: "repo", SourcePath: "repo", Destination: "repo", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "head", BaseOID: "base", Relation: RelationDiverged, WorkingTree: dirty, InitialFingerprint: "planned-content",
	}}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncFailed || result.Repositories[0].Err == nil ||
		!strings.Contains(result.Repositories[0].Err.Error(), "fingerprint changed") {
		t.Fatalf("result = %+v", result)
	}
	if len(git.rebased) != 0 || len(git.recovery) != 0 {
		t.Fatalf("mutations ran: rebases=%v recovery=%v", git.rebased, git.recovery)
	}
}

func TestInterruptedPlanContinuesOnlyRecordedRebase(t *testing.T) {
	controlRoot := t.TempDir()
	git := &fakeGit{
		base: gitops.ResolvedBase{FullRef: "refs/remotes/origin/main", OID: "base"},
		checkouts: map[string]gitops.Checkout{"destination": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "head",
		}},
		rebase: map[string]gitops.SyncResult{"destination": {Status: gitops.SyncConflict, Err: errors.New("conflict")}},
	}
	snapshot := healthySnapshot(t, "repo", "head")
	plan, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot, []RepositoryConfig{{ID: "repo", Remote: "origin", BasePreference: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	recovered, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Repos) != 1 || !recovered.Repos[0].Recovery {
		t.Fatalf("recovered plan = %+v", recovered)
	}
	git.operations = map[string][]gitops.ActiveOperation{"destination": {gitops.OperationRebase}}
	git.rebase["destination"] = gitops.SyncResult{Status: gitops.SyncRebased, To: "new"}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncRebased || git.rebased[len(git.rebased)-1] != "continue:destination:base" {
		t.Fatalf("result = %+v; calls = %v", result, git.rebased)
	}
}

func TestExecutorRetryOfSamePlanContinuesRecordedConflict(t *testing.T) {
	t.Parallel()

	operationPath := t.TempDir() + "/sync.json"
	git := &fakeGit{
		checkouts: map[string]gitops.Checkout{"destination": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "head",
		}},
		rebase: map[string]gitops.SyncResult{"destination": {Status: gitops.SyncConflict, Err: errors.New("conflict")}},
	}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, Repos: []RepositoryPlan{{
		ID: "repo", Destination: "destination", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "head", BaseOID: "base", Relation: RelationDiverged, InitialFingerprint: "clean",
	}}}
	executor := Executor{Git: git, Locker: fakeLocker{}}
	first, err := executor.Execute(context.Background(), plan)
	if err != nil || first.Repositories[0].Status != gitops.SyncConflict {
		t.Fatalf("first Execute = %+v, %v", first, err)
	}
	git.operations = map[string][]gitops.ActiveOperation{"destination": {gitops.OperationRebase}}
	git.rebase["destination"] = gitops.SyncResult{Status: gitops.SyncRebased, To: "new"}

	second, err := executor.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if second.Repositories[0].Status != gitops.SyncRebased || git.rebased[len(git.rebased)-1] != "continue:destination:base" {
		t.Fatalf("second Execute = %+v; calls = %v", second, git.rebased)
	}
}

func TestInterruptedPlanMigratesCleanSchemaV1Conflict(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "sync.json")
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, BuiltAt: time.Unix(1, 0), Repos: []RepositoryPlan{{
		ID: "repo", SourcePath: "source", Destination: "destination", BranchRef: "refs/heads/work",
		PreHeadOID: "head", BaseOID: "base", Relation: RelationDiverged,
	}}}
	legacy := operationRecord{
		SchemaVersion: 1, Kind: "sync-work", Plan: plan, UpdatedAt: time.Unix(2, 0),
		Repositories: []checkpoint{{ID: "repo", Status: "rebasing"}},
	}
	if err := work.CreateJSON(operationPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := interruptedPlan(operationPath, plan.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !recovered.Repos[0].Recovery {
		t.Fatalf("recovered = %+v, ok=%v", recovered, ok)
	}
	stored, err := loadRecord(operationPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SchemaVersion != operationSchemaVersion || stored.Repositories[0].Phase != PhaseRebaseConflict || stored.OperationID == "" {
		t.Fatalf("migrated record = %+v", stored)
	}
}

func TestInterruptedPlanBlocksDirtySchemaV1WithoutRecoveryOID(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "sync.json")
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, Repos: []RepositoryPlan{{
		ID: "repo", WorkingTree: gitops.WorkingTreeStatus{Unstaged: 1},
	}}}
	legacy := operationRecord{
		SchemaVersion: 1, Kind: "sync-work", Plan: plan, UpdatedAt: time.Unix(2, 0),
		Repositories: []checkpoint{{ID: "repo", Status: "rebasing"}},
	}
	if err := work.CreateJSON(operationPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interruptedPlan(operationPath, plan.WorkID); err == nil || !strings.Contains(err.Error(), "no provable recovery OID") {
		t.Fatalf("error = %v", err)
	}
}

func TestInterruptedPlanBlocksDirtySchemaV2WithoutExactFingerprint(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "sync.json")
	operationID := strings.Repeat("a", 32)
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, Repos: []RepositoryPlan{{
		ID: "repo", WorkingTree: gitops.WorkingTreeStatus{Unstaged: 1},
	}}}
	legacy := operationRecord{
		SchemaVersion: 2, OperationID: operationID, Kind: "sync-work", Plan: plan, UpdatedAt: time.Unix(2, 0),
		Repositories: []checkpoint{{ID: "repo", Phase: PhaseRebaseIntent}},
	}
	if err := work.CreateJSON(operationPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := interruptedPlan(operationPath, plan.WorkID); err == nil || !strings.Contains(err.Error(), "no exact initial fingerprint") {
		t.Fatalf("error = %v", err)
	}
}

func TestCleanFailedRebaseWithoutMutationIsRetryable(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "sync.json")
	git := &fakeGit{
		checkouts: map[string]gitops.Checkout{"repo": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "head",
		}},
		rebase: map[string]gitops.SyncResult{"repo": {Status: gitops.SyncFailed, Err: errors.New("hook rejected rebase")}},
	}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, Repos: []RepositoryPlan{{
		ID: "repo", SourcePath: "repo", Destination: "repo", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "head", BaseOID: "base", Relation: RelationDiverged, InitialFingerprint: "clean",
	}}}
	executor := Executor{Git: git, Locker: fakeLocker{}}
	first, err := executor.Execute(context.Background(), plan)
	if err != nil || first.Repositories[0].Status != gitops.SyncFailed {
		t.Fatalf("first Execute = %+v, err=%v", first, err)
	}
	record, err := loadRecord(operationPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.Repositories[0].Phase != PhaseRebaseIntent {
		t.Fatalf("phase after non-mutating failure = %s", record.Repositories[0].Phase)
	}
	git.rebase["repo"] = gitops.SyncResult{Status: gitops.SyncRebased, From: "head", To: "rebased"}
	second, err := executor.Execute(context.Background(), plan)
	if err != nil || second.Repositories[0].Status != gitops.SyncRebased || second.Repositories[0].Err != nil {
		t.Fatalf("second Execute = %+v, err=%v", second, err)
	}
	if len(git.rebased) != 2 {
		t.Fatalf("rebase calls = %v", git.rebased)
	}
}

func TestRollbackResumeRetainsRecoveryAfterUserEdit(t *testing.T) {
	plan, record := dirtyRestoreRecord(t, true)
	checkpoint := record.Repositories[0]
	git := &fakeGit{
		checkouts: map[string]gitops.Checkout{"repo": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "rebased",
		}},
		statuses:     map[string]gitops.WorkingTreeStatus{"repo": plan.Repos[0].WorkingTree},
		fingerprints: map[string]string{"repo": "later-user-edit-with-same-count"},
		recovery:     map[string]string{checkpoint.RecoveryRef: checkpoint.RecoveryOID},
	}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncFailed || result.Repositories[0].Err == nil {
		t.Fatalf("result = %+v", result)
	}
	if git.abortCalls != 0 || git.deleteCalls != 0 || git.recovery[checkpoint.RecoveryRef] != checkpoint.RecoveryOID {
		t.Fatalf("destructive calls=%d deletes=%d refs=%v", git.abortCalls, git.deleteCalls, git.recovery)
	}
}

func TestResumeAfterUncheckpointedRecoveryApplyRetainsRef(t *testing.T) {
	plan, record := dirtyRestoreRecord(t, false)
	checkpoint := record.Repositories[0]
	git := &fakeGit{
		checkouts: map[string]gitops.Checkout{"repo": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: checkpoint.To,
		}},
		statuses:     map[string]gitops.WorkingTreeStatus{"repo": plan.Repos[0].WorkingTree},
		fingerprints: map[string]string{"repo": "recovery-may-already-be-applied"},
		recovery:     map[string]string{checkpoint.RecoveryRef: checkpoint.RecoveryOID},
	}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncFailed || result.Repositories[0].Err == nil ||
		!strings.Contains(result.Repositories[0].Err.Error(), "may have completed") {
		t.Fatalf("result = %+v", result)
	}
	if git.applyCalls != 0 || git.deleteCalls != 0 || git.recovery[checkpoint.RecoveryRef] != checkpoint.RecoveryOID {
		t.Fatalf("applies=%d deletes=%d refs=%v", git.applyCalls, git.deleteCalls, git.recovery)
	}
}

func dirtyRestoreRecord(t *testing.T, rollback bool) (Plan, operationRecord) {
	t.Helper()
	operationPath := filepath.Join(t.TempDir(), "sync.json")
	operationID := strings.Repeat("b", 32)
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, Repos: []RepositoryPlan{{
		ID: "repo", SourcePath: "repo", Destination: "repo", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "head", BaseOID: "base", Relation: RelationDiverged,
		WorkingTree: gitops.WorkingTreeStatus{Unstaged: 1}, InitialFingerprint: "initial",
	}}}
	repositoryCheckpoint := checkpoint{
		ID: "repo", Phase: PhaseRestoreIntent, To: "rebased", Rollback: rollback,
		RecoveryRef: recoveryRef(plan.WorkID, operationID, "repo"), RecoveryOID: "recovery",
		Marker: recoveryMarker(plan.WorkID, operationID, "repo"), OwnedFingerprint: "operation-owned",
	}
	record := operationRecord{
		SchemaVersion: operationSchemaVersion, OperationID: operationID, Kind: "sync-work", Plan: plan,
		Repositories: []checkpoint{repositoryCheckpoint}, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
	}
	if err := work.CreateJSON(operationPath, record, 0o600); err != nil {
		t.Fatal(err)
	}
	return plan, record
}

func TestDirtyConflictRetainsRecoveryRefWhenRollbackIsUnproven(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "sync.json")
	dirty := gitops.WorkingTreeStatus{Unstaged: 1}
	git := &fakeGit{
		checkouts: map[string]gitops.Checkout{"repo": {
			Identity: gitops.RepositoryIdentity{CommonDir: "common"}, FullRef: "refs/heads/work", HeadOID: "head",
		}},
		statuses: map[string]gitops.WorkingTreeStatus{"repo": dirty},
		rebase:   map[string]gitops.SyncResult{"repo": {Status: gitops.SyncConflict, Err: errors.New("conflict")}},
		abortErr: errors.New("cannot prove reset"),
	}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: operationPath, Repos: []RepositoryPlan{{
		ID: "repo", SourcePath: "repo", Destination: "repo", GitCommonDir: "common", BranchRef: "refs/heads/work",
		PreHeadOID: "head", BaseOID: "base", Relation: RelationDiverged, WorkingTree: dirty, InitialFingerprint: "dirty",
	}}}
	result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncFailed || result.Repositories[0].Err == nil {
		t.Fatalf("result = %+v", result)
	}
	record, err := loadRecord(operationPath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := record.Repositories[0]
	if checkpoint.Phase != PhaseFailedKnownState || checkpoint.RecoveryRef == "" || git.recovery[checkpoint.RecoveryRef] != checkpoint.RecoveryOID {
		t.Fatalf("checkpoint = %+v, refs=%v", checkpoint, git.recovery)
	}
}

func TestCancellationDoesNotKillCurrentRebaseAndStopsBeforeNextRepository(t *testing.T) {
	started, releaseRebase := make(chan struct{}), make(chan struct{})
	base := &fakeGit{
		checkouts: map[string]gitops.Checkout{
			"a": {Identity: gitops.RepositoryIdentity{CommonDir: "common-a"}, FullRef: "refs/heads/work", HeadOID: "head-a"},
			"b": {Identity: gitops.RepositoryIdentity{CommonDir: "common-b"}, FullRef: "refs/heads/work", HeadOID: "head-b"},
		},
	}
	git := &cancelGit{fakeGit: base, started: started, release: releaseRebase}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: filepath.Join(t.TempDir(), "sync.json"), Repos: []RepositoryPlan{
		{ID: "a", Destination: "a", GitCommonDir: "common-a", BranchRef: "refs/heads/work", PreHeadOID: "head-a", BaseOID: "base-a", Relation: RelationDiverged, InitialFingerprint: "clean"},
		{ID: "b", Destination: "b", GitCommonDir: "common-b", BranchRef: "refs/heads/work", PreHeadOID: "head-b", BaseOID: "base-b", Relation: RelationDiverged, InitialFingerprint: "clean"},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := (Executor{Git: git, Locker: fakeLocker{}}).Execute(ctx, plan)
		done <- outcome{result: result, err: err}
	}()
	<-started
	cancel()
	close(releaseRebase)
	observed := <-done
	if !errors.Is(observed.err, context.Canceled) {
		t.Fatalf("error = %v", observed.err)
	}
	if len(observed.result.Repositories) != 1 || observed.result.Repositories[0].Status != gitops.SyncRebased {
		t.Fatalf("result = %+v", observed.result)
	}
	if len(git.rebased) != 1 || git.rebased[0] != "a:base-a" {
		t.Fatalf("rebase calls = %v", git.rebased)
	}
}

func TestTimeoutUsesFreshReconciliationContextBeforeStopping(t *testing.T) {
	base := &fakeGit{checkouts: map[string]gitops.Checkout{
		"a": {Identity: gitops.RepositoryIdentity{CommonDir: "common-a"}, FullRef: "refs/heads/work", HeadOID: "head-a"},
		"b": {Identity: gitops.RepositoryIdentity{CommonDir: "common-b"}, FullRef: "refs/heads/work", HeadOID: "head-b"},
	}}
	git := &timeoutGit{fakeGit: base}
	plan := Plan{WorkName: "work", WorkID: "id", OperationRecord: filepath.Join(t.TempDir(), "sync.json"), Repos: []RepositoryPlan{
		{ID: "a", SourcePath: "a", Destination: "a", GitCommonDir: "common-a", BranchRef: "refs/heads/work", PreHeadOID: "head-a", BaseOID: "base-a", Relation: RelationDiverged, InitialFingerprint: "clean"},
		{ID: "b", SourcePath: "b", Destination: "b", GitCommonDir: "common-b", BranchRef: "refs/heads/work", PreHeadOID: "head-b", BaseOID: "base-b", Relation: RelationDiverged, InitialFingerprint: "clean"},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result, err := (Executor{Git: git, Locker: fakeLocker{}, ReconcileFor: time.Second}).Execute(ctx, plan)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Status != gitops.SyncFailed {
		t.Fatalf("result = %+v", result)
	}
	if !git.reconciledWithLiveContext {
		t.Fatal("post-timeout inspection reused the expired mutation context")
	}
	record, loadErr := loadRecord(plan.OperationRecord)
	if loadErr != nil || record.Repositories[0].Phase != PhaseRebaseIntent || record.Repositories[1].Phase != PhasePending {
		t.Fatalf("record = %+v, err=%v", record, loadErr)
	}
}

func TestExecutorPreservesDirtyTreeInPrivateRefAndKeepsUserStashes(t *testing.T) {
	repo, ticketHead, baseOID := setupWorkflowRepository(t, false)
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("user stash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "stash", "push", "--message", "user-owned")
	baseline, err := gitops.StashEntriesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := gitops.WorkingTreeStatusContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := gitops.InspectCheckoutContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	plan := workflowPlan(t, repo, checkout.Identity.CommonDir, ticketHead, baseOID, status)
	result, err := (Executor{Git: SystemGit{}, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Repositories) != 1 || result.Repositories[0].Status != gitops.SyncRebased || result.Repositories[0].Err != nil {
		t.Fatalf("result = %+v", result)
	}
	after, err := gitops.WorkingTreeStatusContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if after != status {
		t.Fatalf("restored status = %+v, want %+v", after, status)
	}
	stashes, err := gitops.StashEntriesContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stashes, baseline) {
		t.Fatalf("user stash stack changed: got %+v want %+v", stashes, baseline)
	}
	refs, err := gitops.ListRecoveryRefsContext(context.Background(), repo, "refs/goworktree/recovery/")
	if err != nil || len(refs) != 0 {
		t.Fatalf("recovery refs = %+v, err=%v", refs, err)
	}
}

func TestExecutorRestoreConflictRollsBackAndRestoresOriginalDirtyTree(t *testing.T) {
	repo, ticketHead, baseOID := setupWorkflowRepository(t, true)
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("local dirty change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := gitops.WorkingTreeStatusContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := gitops.InspectCheckoutContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	plan := workflowPlan(t, repo, checkout.Identity.CommonDir, ticketHead, baseOID, status)
	result, err := (Executor{Git: SystemGit{}, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncRolledBack || result.Repositories[0].Err == nil {
		t.Fatalf("result = %+v", result)
	}
	if head := strings.TrimSpace(syncTestOutput(t, repo, "rev-parse", "HEAD")); head != ticketHead {
		t.Fatalf("HEAD = %s, want %s", head, ticketHead)
	}
	data, err := os.ReadFile(filepath.Join(repo, "shared.txt"))
	if err != nil || string(data) != "local dirty change\n" {
		t.Fatalf("dirty file = %q, err=%v", data, err)
	}
	refs, err := gitops.ListRecoveryRefsContext(context.Background(), repo, "refs/goworktree/recovery/")
	if err != nil || len(refs) != 0 {
		t.Fatalf("recovery refs = %+v, err=%v", refs, err)
	}
}

func TestExecutorDirtyRebaseConflictRollsBackBeforeRestoringChanges(t *testing.T) {
	repo, ticketHead, plan := setupDirtyRebaseConflict(t)
	result, err := (Executor{Git: SystemGit{}, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Repositories[0].Status != gitops.SyncRolledBack || result.Repositories[0].Err == nil {
		t.Fatalf("result = %+v", result)
	}
	if gitopsHasRebase(t, repo) {
		t.Fatal("rebase remains active after dirty rollback")
	}
	if head := strings.TrimSpace(syncTestOutput(t, repo, "rev-parse", "HEAD")); head != ticketHead {
		t.Fatalf("HEAD = %s, want %s", head, ticketHead)
	}
	if _, err := os.Stat(filepath.Join(repo, "dirty.txt")); err != nil {
		t.Fatalf("dirty file was not restored: %v", err)
	}
}

func TestExecutorRetainsInterruptedDirtyRebaseAfterPostCrashEdit(t *testing.T) {
	repo, _, plan := setupDirtyRebaseConflict(t)
	injected := false
	first, err := (Executor{
		Git: SystemGit{}, Locker: fakeLocker{},
		Fault: func(point string) error {
			if !injected && point == "rebase-mutation:repo" {
				injected = true
				return errors.New("crash")
			}
			return nil
		},
	}).Execute(context.Background(), plan)
	if err != nil || !injected || first.Repositories[0].Err == nil {
		t.Fatalf("first Execute = %+v, err=%v, injected=%v", first, err, injected)
	}
	if !gitopsHasRebase(t, repo) {
		t.Fatal("expected interrupted rebase")
	}
	postCrashPath := filepath.Join(repo, "post-crash.txt")
	if err := os.WriteFile(postCrashPath, []byte("must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	resumed, err := (Executor{Git: SystemGit{}, Locker: fakeLocker{}}).Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Repositories[0].Status != gitops.SyncFailed || resumed.Repositories[0].Err == nil ||
		!strings.Contains(resumed.Repositories[0].Err.Error(), "no durable operation-owned fingerprint") {
		t.Fatalf("resumed = %+v", resumed)
	}
	if !gitopsHasRebase(t, repo) {
		t.Fatal("ambiguous rebase was mutated during Resume")
	}
	data, err := os.ReadFile(postCrashPath)
	if err != nil || string(data) != "must survive\n" {
		t.Fatalf("post-crash edit = %q, err=%v", data, err)
	}
	refs, err := gitops.ListRecoveryRefsContext(context.Background(), repo, "refs/goworktree/recovery/")
	if err != nil || len(refs) != 1 {
		t.Fatalf("recovery refs = %+v, err=%v", refs, err)
	}
}

func setupDirtyRebaseConflict(t *testing.T) (repo, ticketHead string, plan Plan) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "init", "-b", "main")
	syncTestGit(t, repo, "config", "user.name", "test")
	syncTestGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "add", "shared.txt")
	syncTestGit(t, repo, "commit", "-m", "initial")
	syncTestGit(t, repo, "checkout", "-b", "ticket")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("ticket\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "commit", "-am", "ticket")
	ticketHead = strings.TrimSpace(syncTestOutput(t, repo, "rev-parse", "HEAD"))
	syncTestGit(t, repo, "checkout", "main")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "commit", "-am", "base")
	baseOID := strings.TrimSpace(syncTestOutput(t, repo, "rev-parse", "HEAD"))
	syncTestGit(t, repo, "checkout", "ticket")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := gitops.WorkingTreeStatusContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := gitops.InspectCheckoutContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return repo, ticketHead, workflowPlan(t, repo, checkout.Identity.CommonDir, ticketHead, baseOID, status)
}

func TestExecutorResumesDirtySyncAtEveryRecoveryBoundary(t *testing.T) {
	points := []string{
		"preserve-intent:repo", "preserve-mutation:repo", "preserved:repo",
		"rebase-intent:repo", "rebase-mutation:repo", "rebased:repo",
		"restore-intent:repo", "restore-mutation:repo", "restore-verified:repo", "recovery-ref-deleted:repo",
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			repo, ticketHead, baseOID := setupWorkflowRepository(t, false)
			if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			syncTestGit(t, repo, "add", "dirty.txt")
			if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("untracked\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			status, err := gitops.WorkingTreeStatusContext(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			checkout, err := gitops.InspectCheckoutContext(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			plan := workflowPlan(t, repo, checkout.Identity.CommonDir, ticketHead, baseOID, status)
			injected := false
			first, err := (Executor{
				Git: SystemGit{}, Locker: fakeLocker{},
				Fault: func(observed string) error {
					if !injected && observed == point {
						injected = true
						return errors.New("crash")
					}
					return nil
				},
			}).Execute(context.Background(), plan)
			if err != nil || !injected || len(first.Repositories) != 1 || first.Repositories[0].Err == nil {
				t.Fatalf("first Execute = %+v, err=%v, injected=%v", first, err, injected)
			}
			ambiguous := point == "preserve-mutation:repo" || point == "rebase-mutation:repo" || point == "restore-mutation:repo"
			postCrashDirectory := filepath.Join(repo, "post-crash-empty")
			if ambiguous {
				if err := os.Mkdir(postCrashDirectory, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			resumed, err := (Executor{Git: SystemGit{}, Locker: fakeLocker{}}).Execute(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if ambiguous {
				if resumed.Repositories[0].Status != gitops.SyncFailed || resumed.Repositories[0].Err == nil {
					t.Fatalf("ambiguous recovery resume = %+v", resumed)
				}
				if info, statErr := os.Stat(postCrashDirectory); statErr != nil || !info.IsDir() {
					t.Fatalf("post-crash directory was changed: info=%v, err=%v", info, statErr)
				}
				refs, refsErr := gitops.ListRecoveryRefsContext(context.Background(), repo, "refs/goworktree/recovery/")
				if refsErr != nil || len(refs) != 1 {
					t.Fatalf("ambiguous recovery refs = %+v, err=%v", refs, refsErr)
				}
				return
			}
			if len(resumed.Repositories) != 1 || resumed.Repositories[0].Status != gitops.SyncRebased || resumed.Repositories[0].Err != nil {
				t.Fatalf("resumed = %+v", resumed)
			}
			after, err := gitops.WorkingTreeStatusContext(context.Background(), repo)
			if err != nil || after != status {
				t.Fatalf("restored status = %+v, err=%v, want %+v", after, err, status)
			}
			refs, err := gitops.ListRecoveryRefsContext(context.Background(), repo, "refs/goworktree/recovery/")
			if err != nil || len(refs) != 0 {
				t.Fatalf("recovery refs = %+v, err=%v", refs, err)
			}
		})
	}
}

func setupWorkflowRepository(t *testing.T, baseChangesShared bool) (repo, ticketHead, baseOID string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "init", "-b", "main")
	syncTestGit(t, repo, "config", "user.name", "test")
	syncTestGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "add", "shared.txt")
	syncTestGit(t, repo, "commit", "-m", "initial")
	syncTestGit(t, repo, "checkout", "-b", "ticket")
	if !baseChangesShared {
		if err := os.WriteFile(filepath.Join(repo, "ticket.txt"), []byte("ticket\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		syncTestGit(t, repo, "add", "ticket.txt")
		syncTestGit(t, repo, "commit", "-m", "ticket")
	}
	ticketHead = strings.TrimSpace(syncTestOutput(t, repo, "rev-parse", "HEAD"))
	syncTestGit(t, repo, "checkout", "main")
	name, content := "base.txt", "base\n"
	if baseChangesShared {
		name, content = "shared.txt", "base change\n"
	}
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTestGit(t, repo, "add", name)
	syncTestGit(t, repo, "commit", "-m", "base")
	baseOID = strings.TrimSpace(syncTestOutput(t, repo, "rev-parse", "HEAD"))
	syncTestGit(t, repo, "checkout", "ticket")
	return repo, ticketHead, baseOID
}

func workflowPlan(t *testing.T, repo, commonDir, head, base string, status gitops.WorkingTreeStatus) Plan {
	t.Helper()
	fingerprint, err := gitops.WorktreeFingerprintContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return Plan{
		WorkName: "work", WorkID: "0123456789abcdef", WorkRoot: filepath.Dir(repo),
		OperationRecord: filepath.Join(t.TempDir(), "operations", "sync-work", "operation.json"),
		Repos: []RepositoryPlan{{
			ID: "repo", SourcePath: repo, GitCommonDir: commonDir, Destination: repo,
			BranchRef: "refs/heads/ticket", PreHeadOID: head, BaseOID: base,
			Relation: RelationDiverged, WorkingTree: status, InitialFingerprint: fingerprint,
		}},
	}
}

func syncTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func syncTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func gitopsHasRebase(t *testing.T, repo string) bool {
	t.Helper()
	operations, err := gitops.ActiveOperationsContext(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range operations {
		if operation == gitops.OperationRebase {
			return true
		}
	}
	return false
}

func healthySnapshot(t *testing.T, id, head string) inspectwork.Snapshot {
	t.Helper()
	name, err := work.ParseName("work")
	if err != nil {
		t.Fatal(err)
	}
	intent := work.RepositoryIntent{ID: id, SourcePath: "source", GitCommonDir: "common", BaseRef: "main", BaseOID: "old", BranchRef: "refs/heads/work", Destination: "destination"}
	manifest := &work.Manifest{Repositories: []work.RepositoryIntent{intent}}
	return inspectwork.Snapshot{
		WorkName: name, WorkID: work.NewIdentity("/works", name), WorkRoot: "/works/work",
		Manifest: inspectwork.ManifestSnapshot{State: inspectwork.MetadataValid, Value: manifest},
		Repositories: []inspectwork.RepositorySnapshot{{
			ID: id, Intent: intent, SourceKnown: true, CheckoutKnown: true, WorkingTreeKnown: true, GitOperationsKnown: true,
			Checkout: inspectwork.CheckoutSnapshot{HeadOID: head, FullRef: intent.BranchRef},
		}},
	}
}

type fakeGit struct {
	base         gitops.ResolvedBase
	fetched      string
	checkouts    map[string]gitops.Checkout
	rebase       map[string]gitops.SyncResult
	rebased      []string
	operations   map[string][]gitops.ActiveOperation
	statuses     map[string]gitops.WorkingTreeStatus
	fingerprints map[string]string
	stashes      []gitops.StashEntry
	recovery     map[string]string
	abortErr     error
	abortCalls   int
	applyCalls   int
	deleteCalls  int
}

func (f *fakeGit) FetchRemote(_ context.Context, repo, remote string) error {
	f.fetched = repo + ":" + remote
	return nil
}
func (f *fakeGit) ResolveBase(context.Context, string, string, string) (gitops.ResolvedBase, error) {
	return f.base, nil
}
func (f *fakeGit) IsAncestor(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (f *fakeGit) InspectCheckout(_ context.Context, path string) (gitops.Checkout, error) {
	return f.checkouts[path], nil
}
func (f *fakeGit) WorkingTreeStatus(_ context.Context, path string) (gitops.WorkingTreeStatus, error) {
	return f.statuses[path], nil
}
func (f *fakeGit) WorktreeFingerprint(_ context.Context, path string) (string, error) {
	if fingerprint := f.fingerprints[path]; fingerprint != "" {
		return fingerprint, nil
	}
	if workingTreeDirty(f.statuses[path]) {
		return "dirty", nil
	}
	return "clean", nil
}
func (f *fakeGit) ActiveOperations(_ context.Context, path string) ([]gitops.ActiveOperation, error) {
	return f.operations[path], nil
}
func (f *fakeGit) RebaseToOID(_ context.Context, path, _ string, oid string) gitops.SyncResult {
	f.rebased = append(f.rebased, path+":"+oid)
	result := f.rebase[path]
	f.recordSuccessfulRebase(path, result)
	return result
}
func (f *fakeGit) ContinueRebase(_ context.Context, path, _ string, oid string) gitops.SyncResult {
	f.rebased = append(f.rebased, "continue:"+path+":"+oid)
	result := f.rebase[path]
	f.recordSuccessfulRebase(path, result)
	return result
}
func (f *fakeGit) recordSuccessfulRebase(path string, result gitops.SyncResult) {
	if result.Status != gitops.SyncRebased && result.Status != gitops.SyncUpToDate {
		return
	}
	checkout := f.checkouts[path]
	if result.To != "" {
		checkout.HeadOID = result.To
	}
	f.checkouts[path] = checkout
	if f.statuses != nil {
		f.statuses[path] = gitops.WorkingTreeStatus{}
	}
	if f.fingerprints != nil {
		f.fingerprints[path] = "clean"
	}
}
func (f *fakeGit) Stashes(context.Context, string) ([]gitops.StashEntry, error) {
	return append([]gitops.StashEntry(nil), f.stashes...), nil
}
func (f *fakeGit) Preserve(_ context.Context, repo, ref, _ string, _ []gitops.StashEntry) (string, error) {
	if f.recovery == nil {
		f.recovery = make(map[string]string)
	}
	f.recovery[ref] = "recovery"
	if f.statuses != nil {
		f.statuses[repo] = gitops.WorkingTreeStatus{}
	}
	if f.fingerprints != nil {
		f.fingerprints[repo] = "clean"
	}
	return "recovery", nil
}
func (f *fakeGit) RecoveryOID(_ context.Context, _ string, ref string) (string, bool, error) {
	oid, ok := f.recovery[ref]
	return oid, ok, nil
}
func (f *fakeGit) ApplyRecovery(context.Context, string, string) error {
	f.applyCalls++
	return nil
}
func (f *fakeGit) DeleteRecoveryRef(_ context.Context, _ string, ref, oid string) error {
	f.deleteCalls++
	if f.recovery[ref] != oid {
		return errors.New("recovery changed")
	}
	delete(f.recovery, ref)
	return nil
}
func (f *fakeGit) AbortAndReset(context.Context, string, string, string) error {
	f.abortCalls++
	return f.abortErr
}
func (f *fakeGit) RecoveryRefs(_ context.Context, _ string, prefix string) ([]gitops.RecoveryRef, error) {
	var refs []gitops.RecoveryRef
	for ref, oid := range f.recovery {
		if strings.HasPrefix(ref, prefix) {
			refs = append(refs, gitops.RecoveryRef{Ref: ref, OID: oid})
		}
	}
	return refs, nil
}

type cancelGit struct {
	*fakeGit
	started chan struct{}
	release chan struct{}
}

func (f *cancelGit) RebaseToOID(ctx context.Context, path, _ string, oid string) gitops.SyncResult {
	f.rebased = append(f.rebased, path+":"+oid)
	close(f.started)
	select {
	case <-ctx.Done():
		return gitops.SyncResult{Status: gitops.SyncFailed, Err: ctx.Err()}
	case <-f.release:
		result := gitops.SyncResult{Status: gitops.SyncRebased, From: "head-a", To: "new-a"}
		f.recordSuccessfulRebase(path, result)
		return result
	}
}

type timeoutGit struct {
	*fakeGit
	timedOut                  bool
	reconciledWithLiveContext bool
}

func (f *timeoutGit) RebaseToOID(ctx context.Context, path, _ string, oid string) gitops.SyncResult {
	f.rebased = append(f.rebased, path+":"+oid)
	<-ctx.Done()
	f.timedOut = true
	return gitops.SyncResult{Status: gitops.SyncFailed, Err: ctx.Err()}
}

func (f *timeoutGit) ActiveOperations(ctx context.Context, path string) ([]gitops.ActiveOperation, error) {
	if f.timedOut && ctx.Err() == nil {
		f.reconciledWithLiveContext = true
	}
	return f.fakeGit.ActiveOperations(ctx, path)
}

type fakeLocker struct{}

func (fakeLocker) AcquireExecution(context.Context, string, []string) (func() error, error) {
	return func() error { return nil }, nil
}
