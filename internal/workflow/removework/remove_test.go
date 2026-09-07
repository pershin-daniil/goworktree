package removework

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/inspectwork"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func TestPlannerRequiresCompletedNewWorkRecord(t *testing.T) {
	for _, test := range []struct {
		name  string
		state inspectwork.MetadataState
		phase string
		want  string
	}{
		{name: "missing", state: inspectwork.MetadataAbsent, want: "not valid"},
		{name: "corrupt", state: inspectwork.MetadataInvalid, want: "not valid"},
		{name: "incomplete", state: inspectwork.MetadataValid, phase: "verifying", want: "not complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, controlRoot, git := plannerSnapshot(t)
			snapshot.Operation.State, snapshot.Operation.Phase = test.state, test.phase
			_, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want %q", err, test.want)
			}
			if len(git.calls) != 0 {
				t.Fatalf("Git calls = %v", git.calls)
			}
		})
	}
}

func TestPlannerRecordsExactFingerprints(t *testing.T) {
	snapshot, controlRoot, git := plannerSnapshot(t)
	plan, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if plan.FingerprintVersion != fingerprintVersion || plan.RootFingerprint == "" {
		t.Fatalf("plan fingerprint metadata = version %d, root %q", plan.FingerprintVersion, plan.RootFingerprint)
	}
	if len(plan.Repositories) != 1 || plan.Repositories[0].WorkingTreeFingerprint != git.fingerprint {
		t.Fatalf("repository plan = %+v", plan.Repositories)
	}
	if git.fingerprintCalls != 1 {
		t.Fatalf("fingerprint calls = %d", git.fingerprintCalls)
	}
}

func TestPlannerIncludesRetainedInactiveBranchWithoutTreatingDestinationAsManaged(t *testing.T) {
	snapshot, controlRoot, git := plannerSnapshot(t)
	inactive := work.RepositoryIntent{
		ID: "retained", SourcePath: filepath.Join(filepath.Dir(snapshot.WorkRoot), "retained-source"), GitCommonDir: "retained-common",
		BranchRef: "refs/heads/work", Destination: filepath.Join(snapshot.WorkRoot, "retained"),
	}
	snapshot.Manifest.Value.InactiveRepositories = []work.InactiveRepositoryIntent{{
		Repository: inactive, BranchRetained: true, BranchOID: "retained-head",
	}}
	git.identities = map[string]gitops.RepositoryIdentity{
		inactive.SourcePath: {SourcePath: inactive.SourcePath, CommonDir: inactive.GitCommonDir},
	}
	git.branchOIDs = map[string]string{inactive.BranchRef: "retained-head"}
	git.branchExistsByRef = map[string]bool{inactive.BranchRef: true}

	plan, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 2 || !plan.Repositories[1].BranchOnly || plan.Repositories[1].ID != "retained" ||
		plan.Repositories[1].WorkingTreeFingerprint != "" || plan.Repositories[1].Destination != "" {
		t.Fatalf("retained branch plan = %+v", plan.Repositories)
	}
}

func TestPlannerAllowsAlreadyMissingRetainedBranch(t *testing.T) {
	snapshot, controlRoot, git := plannerSnapshot(t)
	intent := work.RepositoryIntent{ID: "retained", SourcePath: filepath.Join(filepath.Dir(snapshot.WorkRoot), "retained-source"), GitCommonDir: "retained-common", BranchRef: "refs/heads/work"}
	snapshot.Manifest.Value.InactiveRepositories = []work.InactiveRepositoryIntent{{Repository: intent, BranchRetained: true, BranchOID: "retained-head"}}
	git.identities = map[string]gitops.RepositoryIdentity{intent.SourcePath: {SourcePath: intent.SourcePath, CommonDir: intent.GitCommonDir}}

	plan, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 2 || !plan.Repositories[1].BranchOnly || plan.Repositories[1].BranchOID != "retained-head" {
		t.Fatalf("retained missing-branch plan = %+v", plan.Repositories)
	}
}

func TestPlannerRejectsMovedOrCheckedOutRetainedBranch(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*fakeGit, work.RepositoryIntent)
		want  string
	}{
		{
			name: "moved", want: "branch changed",
			setup: func(git *fakeGit, intent work.RepositoryIntent) {
				git.branchOIDs = map[string]string{intent.BranchRef: "moved"}
				git.branchExistsByRef = map[string]bool{intent.BranchRef: true}
			},
		},
		{
			name: "checked out elsewhere", want: "checked out elsewhere",
			setup: func(git *fakeGit, intent work.RepositoryIntent) {
				git.branchOIDs = map[string]string{intent.BranchRef: "retained-head"}
				git.branchExistsByRef = map[string]bool{intent.BranchRef: true}
				git.registrations = map[string][]gitops.WorktreeRegistration{intent.SourcePath: {{Path: "/elsewhere", Branch: intent.BranchRef, HeadOID: "retained-head"}}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, controlRoot, git := plannerSnapshot(t)
			intent := work.RepositoryIntent{ID: "retained", SourcePath: filepath.Join(filepath.Dir(snapshot.WorkRoot), "retained-source"), GitCommonDir: "retained-common", BranchRef: "refs/heads/work"}
			snapshot.Manifest.Value.InactiveRepositories = []work.InactiveRepositoryIntent{{Repository: intent, BranchRetained: true, BranchOID: "retained-head"}}
			git.identities = map[string]gitops.RepositoryIdentity{intent.SourcePath: {SourcePath: intent.SourcePath, CommonDir: intent.GitCommonDir}}
			test.setup(git, intent)
			if _, err := (Planner{Git: git}).Build(context.Background(), snapshot, controlRoot); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExecutorRequiresExactConfirmationBeforeRecordingOrMutation(t *testing.T) {
	plan, git := testPlan(t)
	_, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, "wrong")
	if err == nil {
		t.Fatal("expected confirmation error")
	}
	if len(git.calls) != 0 {
		t.Fatalf("Git calls = %v", git.calls)
	}
	if _, err := os.Stat(plan.OperationRecord); !os.IsNotExist(err) {
		t.Fatalf("operation record exists before confirmation: %v", err)
	}
}

func TestExecutorRemovesExactWorktreeBranchAndRoot(t *testing.T) {
	plan, git := testPlan(t)
	result, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RootRemoved || len(result.Repositories) != 1 || result.Repositories[0].Status != "removed" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Lstat(plan.WorkRoot); !os.IsNotExist(err) {
		t.Fatalf("Work root still exists: %v", err)
	}
	if len(git.calls) != 2 || git.calls[0] != "remove" || git.calls[1] != "delete" {
		t.Fatalf("calls = %v", git.calls)
	}
	if _, err := os.Stat(plan.OperationRecord); !os.IsNotExist(err) {
		t.Fatalf("active Remove record remains after archive publication: %v", err)
	}
	if result.ArchiveID == "" || result.ArchivePath == "" {
		t.Fatalf("archive metadata missing: %+v", result)
	}
}

func TestExecutorArchivesAndCleansCompletedChangeWorkRecord(t *testing.T) {
	plan, git := testPlan(t)
	changeRecord := changeWorkRecord(plan)
	if err := os.MkdirAll(filepath.Dir(changeRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changeRecord, []byte("{\"change\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(changeRecord); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active Change Work record remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.ArchivePath, "change-work.json")); err != nil {
		t.Fatalf("archived Change Work record: %v", err)
	}
}

func TestExecutorRejectsDifferentWorkingTreeWithSameAggregateStatus(t *testing.T) {
	plan, git := testPlan(t)
	git.fingerprint = "same-counts-different-paths-and-content"

	_, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err == nil || !strings.Contains(err.Error(), "working tree changed after confirmation") {
		t.Fatalf("Execute error = %v", err)
	}
	if len(git.calls) != 0 {
		t.Fatalf("destructive Git calls = %v", git.calls)
	}
	if _, err := os.Lstat(plan.Repositories[0].Destination); err != nil {
		t.Fatalf("worktree was removed: %v", err)
	}
}

func TestExecutorRevalidatesWorkingTreeImmediatelyBeforeRemoval(t *testing.T) {
	plan, git := testPlan(t)
	git.fingerprints = []string{plan.Repositories[0].WorkingTreeFingerprint, "changed-after-intent"}

	_, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err == nil || !strings.Contains(err.Error(), "working tree changed after confirmation") {
		t.Fatalf("Execute error = %v", err)
	}
	if len(git.calls) != 0 {
		t.Fatalf("destructive Git calls = %v", git.calls)
	}
}

func TestExecutorDeletesRetainedBranchOnlyAndResumesBeforeOrAfterDeletion(t *testing.T) {
	for _, point := range []string{"branch-intent:repo", "branch-mutation:repo"} {
		t.Run(point, func(t *testing.T) {
			plan, git := testPlan(t)
			plan.Repositories[0].BranchOnly = true
			plan.Repositories[0].Destination = ""
			plan.Repositories[0].WorkingTreeFingerprint = ""
			refreshRootFingerprint(t, &plan)
			git.plan = plan.Repositories[0]
			git.worktreeExists = false
			injected := false
			executor := Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}, Fault: func(observed string) error {
				if !injected && observed == point {
					injected = true
					return errors.New("crash")
				}
				return nil
			}}
			if _, err := executor.Execute(context.Background(), plan, plan.WorkName); err == nil || !injected {
				t.Fatalf("first Execute error=%v, injected=%v", err, injected)
			}
			result, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
			if err != nil {
				t.Fatal(err)
			}
			if !result.RootRemoved || git.branchExists {
				t.Fatalf("resumed result=%+v branchExists=%v", result, git.branchExists)
			}
			if got := strings.Join(git.calls, ","); got != "delete" {
				t.Fatalf("Git calls = %q, want only branch deletion", got)
			}
		})
	}
}

func TestExecutorRejectsMovedOrCheckedOutRetainedBranchBeforeDeletion(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*fakeGit, RepositoryPlan)
		want  string
	}{
		{
			name: "moved", want: "different commit after confirmation",
			setup: func(git *fakeGit, plan RepositoryPlan) {
				git.branchOIDs = map[string]string{plan.BranchRef: "moved"}
				git.branchExistsByRef = map[string]bool{plan.BranchRef: true}
			},
		},
		{
			name: "checked out elsewhere", want: "checked out elsewhere",
			setup: func(git *fakeGit, plan RepositoryPlan) {
				git.registrations = map[string][]gitops.WorktreeRegistration{plan.SourcePath: {{Path: "/elsewhere", Branch: plan.BranchRef, HeadOID: plan.BranchOID}}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, git := testPlan(t)
			plan.Repositories[0].BranchOnly = true
			plan.Repositories[0].Destination = ""
			plan.Repositories[0].WorkingTreeFingerprint = ""
			refreshRootFingerprint(t, &plan)
			git.plan = plan.Repositories[0]
			git.worktreeExists = false
			test.setup(git, plan.Repositories[0])

			if _, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Execute error = %v, want %q", err, test.want)
			}
			if len(git.calls) != 0 {
				t.Fatalf("destructive Git calls = %v", git.calls)
			}
		})
	}
}

func TestExecutorRejectsChangedRootContents(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*testing.T, string)
		mutate func(*testing.T, string)
	}{
		{
			name: "changed file",
			setup: func(t *testing.T, root string) {
				t.Helper()
				writeTestFile(t, filepath.Join(root, "notes.txt"), "before")
			},
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeTestFile(t, filepath.Join(root, "notes.txt"), "after-")
			},
		},
		{
			name: "nested file added",
			setup: func(t *testing.T, root string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(root, "notes", "nested"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			mutate: func(t *testing.T, root string) {
				t.Helper()
				writeTestFile(t, filepath.Join(root, "notes", "nested", "new.txt"), "new")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, git := testPlan(t)
			test.setup(t, plan.WorkRoot)
			refreshRootFingerprint(t, &plan)
			test.mutate(t, plan.WorkRoot)

			_, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
			if err == nil || !strings.Contains(err.Error(), "Work root contents changed") {
				t.Fatalf("Execute error = %v", err)
			}
			if _, err := os.Lstat(plan.WorkRoot); err != nil {
				t.Fatalf("Work root was removed: %v", err)
			}
		})
	}
}

func TestExecutorRevalidatesRootImmediatelyBeforeRemoval(t *testing.T) {
	plan, git := testPlan(t)
	changedPath := filepath.Join(plan.WorkRoot, "arrived-after-intent.txt")
	executor := Executor{
		Git: git, Locker: fakeLocker{}, Safety: fakeSafety{},
		Fault: func(point string) error {
			if point == "root-intent" {
				return os.WriteFile(changedPath, []byte("preserve"), 0o600)
			}
			return nil
		},
	}

	_, err := executor.Execute(context.Background(), plan, plan.WorkName)
	if err == nil || !strings.Contains(err.Error(), "changed immediately before removal") {
		t.Fatalf("Execute error = %v", err)
	}
	if data, err := os.ReadFile(changedPath); err != nil || string(data) != "preserve" {
		t.Fatalf("new root data was not preserved: data=%q err=%v", data, err)
	}
}

func TestExecutorMigratesSchemaV1AndResumesCompletedRemoval(t *testing.T) {
	plan, git := testPlan(t)
	if err := os.RemoveAll(plan.WorkRoot); err != nil {
		t.Fatal(err)
	}
	git.worktreeExists, git.branchExists = false, false
	plan.FingerprintVersion = 0
	plan.RootFingerprint = ""
	plan.Repositories[0].WorkingTreeFingerprint = ""
	legacy := operationRecord{
		SchemaVersion: 1, Kind: "remove-work", Plan: plan, UpdatedAt: time.Unix(1, 0),
		Repositories: []checkpoint{{ID: "repo", WorktreeRemoved: true, BranchDeleted: true}}, RootRemoved: true,
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := work.CreateJSON(plan.OperationRecord, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err != nil {
		t.Fatal(err)
	}
	if len(git.calls) != 0 {
		t.Fatalf("Git calls = %v", git.calls)
	}
	if result.ArchiveID == "" || !result.RootRemoved {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecutorDoesNotAuthorizePendingLegacyDestruction(t *testing.T) {
	plan, git := testPlan(t)
	plan.FingerprintVersion = 0
	plan.RootFingerprint = ""
	plan.Repositories[0].WorkingTreeFingerprint = ""
	legacy := operationRecord{
		SchemaVersion: 2, RemovalID: strings.Repeat("0", 32), ArchiveID: "work-20260101T000000Z-01234567",
		Kind: "remove-work", Phase: PhaseWorktrees, Plan: plan, Root: StepPending, UpdatedAt: time.Unix(1, 0),
		Repositories: []checkpoint{{ID: "repo", Worktree: StepPending, Branch: StepPending}},
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := work.CreateJSON(plan.OperationRecord, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err == nil || !strings.Contains(err.Error(), "legacy Remove Work operation lacks an exact working tree fingerprint") {
		t.Fatalf("Execute error = %v", err)
	}
	if len(git.calls) != 0 {
		t.Fatalf("destructive Git calls = %v", git.calls)
	}
	migrated, err := loadRecord(plan.OperationRecord)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != operationSchemaVersion || migrated.Plan.FingerprintVersion != 0 || migrated.Plan.Repositories[0].WorkingTreeFingerprint != "" {
		t.Fatalf("legacy record gained destructive authorization: %+v", migrated)
	}
}

func TestExecutorDoesNotAuthorizePendingLegacyRootRemoval(t *testing.T) {
	plan, git := testPlan(t)
	if err := os.RemoveAll(plan.Repositories[0].Destination); err != nil {
		t.Fatal(err)
	}
	git.worktreeExists, git.branchExists = false, false
	plan.FingerprintVersion = 0
	plan.RootFingerprint = ""
	plan.Repositories[0].WorkingTreeFingerprint = ""
	legacy := operationRecord{
		SchemaVersion: 2, RemovalID: strings.Repeat("0", 32), ArchiveID: "work-20260101T000000Z-01234567",
		Kind: "remove-work", Phase: PhaseRoot, Plan: plan, Root: StepPending, UpdatedAt: time.Unix(1, 0),
		Repositories: []checkpoint{{ID: "repo", Worktree: StepCompleted, Branch: StepCompleted}},
	}
	if err := os.MkdirAll(filepath.Dir(plan.OperationRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := work.CreateJSON(plan.OperationRecord, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
	if err == nil || !strings.Contains(err.Error(), "legacy Remove Work operation lacks an exact Work root fingerprint") {
		t.Fatalf("Execute error = %v", err)
	}
	if _, err := os.Lstat(plan.WorkRoot); err != nil {
		t.Fatalf("Work root was removed: %v", err)
	}
}

func TestExecutorResumesAfterFaultAtEveryRemovalBoundary(t *testing.T) {
	points := []string{
		"worktree-intent:repo", "worktree-mutation:repo", "worktree-completed:repo",
		"branch-intent:repo", "branch-mutation:repo", "branch-completed:repo",
		"root-intent", "root-mutation", "root-completed", "archive-published",
		"active-new-work-deleted", "active-sync-work-deleted", "active-change-work-deleted", "active-remove-work-deleted",
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			plan, git := testPlan(t)
			syncRecord := syncWorkRecord(plan)
			if err := os.MkdirAll(filepath.Dir(syncRecord), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(syncRecord, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			changeRecord := changeWorkRecord(plan)
			if err := os.MkdirAll(filepath.Dir(changeRecord), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(changeRecord, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			injected := false
			executor := Executor{
				Git: git, Locker: fakeLocker{}, Safety: fakeSafety{},
				Fault: func(observed string) error {
					if !injected && observed == point {
						injected = true
						return errors.New("crash")
					}
					return nil
				},
			}
			if _, err := executor.Execute(context.Background(), plan, plan.WorkName); err == nil || !injected {
				t.Fatalf("first Execute error=%v, injected=%v", err, injected)
			}
			if point == "active-remove-work-deleted" {
				if _, err := os.Lstat(plan.OperationRecord); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("terminal Remove record remains: %v", err)
				}
				return
			}
			result, err := (Executor{Git: git, Locker: fakeLocker{}, Safety: fakeSafety{}}).Execute(context.Background(), plan, plan.WorkName)
			if err != nil {
				t.Fatal(err)
			}
			if !result.RootRemoved || result.ArchiveID == "" || result.ArchivePath == "" {
				t.Fatalf("resumed result = %+v", result)
			}
			if _, err := os.Lstat(plan.OperationRecord); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("active Remove record remains: %v", err)
			}
		})
	}
}

func testPlan(t *testing.T) (Plan, *fakeGit) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	destination := filepath.Join(workRoot, "repo")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workRoot, ".goworktree.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		WorkName: "work", WorkID: "id", WorkRoot: workRoot,
		OperationRecord: filepath.Join(root, "control", "operations", "remove-work", "id.json"), Irreversible: true,
		FingerprintVersion: fingerprintVersion,
		RootEntries:        []Entry{{Name: ".goworktree.json", Kind: "file"}, {Name: "repo", Kind: "directory"}},
		Repositories: []RepositoryPlan{{
			ID: "repo", SourcePath: filepath.Join(root, "source"), GitCommonDir: "common", Destination: destination,
			BranchRef: "refs/heads/work", BranchOID: "head", WorkingTreeFingerprint: "fingerprint",
		}},
	}
	refreshRootFingerprint(t, &plan)
	git := &fakeGit{plan: plan.Repositories[0], branchExists: true, worktreeExists: true, fingerprint: "fingerprint"}
	newRecord := filepath.Join(root, "control", "operations", "new-work", "id.json")
	if err := os.MkdirAll(filepath.Dir(newRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newRecord, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return plan, git
}

func plannerSnapshot(t *testing.T) (inspectwork.Snapshot, string, *fakeGit) {
	t.Helper()
	root := t.TempDir()
	workRoot := filepath.Join(root, "work")
	destination := filepath.Join(workRoot, "repo")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workRoot, ".goworktree.json"), "{}")
	name, err := work.ParseName("work")
	if err != nil {
		t.Fatal(err)
	}
	workID := work.NewIdentity(root, name)
	intent := work.RepositoryIntent{
		ID: "repo", SourcePath: filepath.Join(root, "source"), GitCommonDir: "common",
		BranchRef: "refs/heads/work", Destination: destination,
	}
	manifest := &work.Manifest{WorkID: workID, Name: name, Repositories: []work.RepositoryIntent{intent}}
	snapshot := inspectwork.Snapshot{
		WorkName: name, WorkID: workID, WorkRoot: workRoot, WorkRootKind: inspectwork.PathDirectory,
		Manifest:  inspectwork.ManifestSnapshot{State: inspectwork.MetadataValid, Value: manifest},
		Operation: inspectwork.OperationSnapshot{State: inspectwork.MetadataValid, Phase: string(newwork.PhaseCreated)},
		Repositories: []inspectwork.RepositorySnapshot{{
			ID: "repo", Intent: intent, SourceKnown: true, BranchKnown: true, BranchExists: true,
			CheckoutKnown: true, WorkingTreeKnown: true, GitOperationsKnown: true, RegistrationsKnown: true,
			BranchOID: "head",
		}},
	}
	git := &fakeGit{fingerprint: "fingerprint"}
	return snapshot, filepath.Join(root, "control"), git
}

func refreshRootFingerprint(t *testing.T, plan *Plan) {
	t.Helper()
	repositories := make([]inspectwork.RepositorySnapshot, 0, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		repositories = append(repositories, inspectwork.RepositorySnapshot{
			Intent: work.RepositoryIntent{Destination: repository.Destination},
		})
	}
	_, fingerprint, err := inspectEntries(plan.WorkRoot, repositories)
	if err != nil {
		t.Fatal(err)
	}
	plan.RootFingerprint = fingerprint
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

type fakeGit struct {
	plan              RepositoryPlan
	branchExists      bool
	worktreeExists    bool
	fingerprint       string
	fingerprints      []string
	fingerprintCalls  int
	calls             []string
	identities        map[string]gitops.RepositoryIdentity
	branchOIDs        map[string]string
	branchExistsByRef map[string]bool
	registrations     map[string][]gitops.WorktreeRegistration
}

func (f *fakeGit) InspectRepository(_ context.Context, path string) (gitops.RepositoryIdentity, error) {
	if identity, ok := f.identities[path]; ok {
		return identity, nil
	}
	return gitops.RepositoryIdentity{SourcePath: path, CommonDir: f.plan.GitCommonDir}, nil
}
func (f *fakeGit) InspectCheckout(context.Context, string) (gitops.Checkout, error) {
	return gitops.Checkout{Identity: gitops.RepositoryIdentity{CommonDir: f.plan.GitCommonDir}, FullRef: f.plan.BranchRef, HeadOID: f.plan.BranchOID}, nil
}
func (f *fakeGit) WorktreeFingerprint(context.Context, string) (string, error) {
	f.fingerprintCalls++
	if len(f.fingerprints) > 0 {
		fingerprint := f.fingerprints[0]
		f.fingerprints = f.fingerprints[1:]
		return fingerprint, nil
	}
	return f.fingerprint, nil
}
func (f *fakeGit) ActiveOperations(context.Context, string) ([]gitops.ActiveOperation, error) {
	return nil, nil
}
func (f *fakeGit) ListWorktrees(_ context.Context, source string) ([]gitops.WorktreeRegistration, error) {
	if registrations, ok := f.registrations[source]; ok {
		return registrations, nil
	}
	if !f.worktreeExists {
		return nil, nil
	}
	return []gitops.WorktreeRegistration{{Path: f.plan.Destination, Branch: f.plan.BranchRef, HeadOID: f.plan.BranchOID}}, nil
}
func (f *fakeGit) LocalBranchOID(_ context.Context, _, branchRef string) (string, bool, error) {
	if oid, ok := f.branchOIDs[branchRef]; ok {
		return oid, f.branchExistsByRef[branchRef], nil
	}
	return f.plan.BranchOID, f.branchExists, nil
}
func (f *fakeGit) RemoveWorktree(_ context.Context, _, destination string) error {
	f.calls = append(f.calls, "remove")
	f.worktreeExists = false
	return os.RemoveAll(destination)
}
func (f *fakeGit) DeleteLocalBranchAtOID(_ context.Context, _ string, branchRef string, _ string) error {
	f.calls = append(f.calls, "delete")
	f.branchExists = false
	if f.branchExistsByRef != nil {
		f.branchExistsByRef[branchRef] = false
	}
	return nil
}

type fakeLocker struct{}

func (fakeLocker) AcquireExecution(context.Context, string, []string) (func() error, error) {
	return func() error { return nil }, nil
}

type fakeSafety struct{ err error }

func (f fakeSafety) Check(context.Context, Plan) error { return f.err }
