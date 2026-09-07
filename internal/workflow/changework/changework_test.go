package changework

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gitops "github.com/pershin-daniil/goworktree/internal/git"
	lockops "github.com/pershin-daniil/goworktree/internal/lock"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func TestAddRemoveAndReattachRepository(t *testing.T) {
	fixture := newFixture(t)
	planner := Planner{Git: SystemGit{}}
	add, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatalf("BuildAdd: %v", err)
	}
	if add.Repositories[0].Reattach {
		t.Fatal("fresh repository planned as reattach")
	}
	result, err := fixture.executor().Execute(context.Background(), add)
	if err != nil {
		t.Fatalf("Execute add: %v", err)
	}
	if result.Revision != 1 {
		t.Fatalf("add revision = %d", result.Revision)
	}
	manifest := fixture.loadManifest(t)
	if manifest.SchemaVersion != work.ManifestSchemaVersion || len(manifest.Repositories) != 2 {
		t.Fatalf("manifest after add = %+v", manifest)
	}
	record, err := LoadRecord(add.OperationPath)
	if err != nil || !RecordMatchesManifest(record, manifest) {
		t.Fatalf("manifest is not linked to latest Change Work operation: record=%+v err=%v", record, err)
	}
	if data, err := os.ReadFile(filepath.Join(fixture.workRoot, "go.work")); err != nil || !strings.Contains(string(data), "./web") {
		t.Fatalf("go.work after add = %q, %v", data, err)
	}

	remove, err := planner.BuildRemove(context.Background(), fixture.catalog("web"), RemoveRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"},
	})
	if err != nil {
		t.Fatalf("BuildRemove: %v", err)
	}
	if _, err := fixture.executor().Execute(context.Background(), remove); err != nil {
		t.Fatalf("Execute remove: %v", err)
	}
	manifest = fixture.loadManifest(t)
	if len(manifest.Repositories) != 1 || len(manifest.InactiveRepositories) != 1 || !manifest.InactiveRepositories[0].BranchRetained {
		t.Fatalf("manifest after remove = %+v", manifest)
	}
	if _, exists, err := gitops.LocalBranchOIDContext(context.Background(), fixture.web, "refs/heads/"+fixture.name.String()); err != nil || !exists {
		t.Fatalf("retained branch exists = %v, %v", exists, err)
	}

	readd, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatalf("BuildAdd reattach: %v", err)
	}
	if !readd.Repositories[0].Reattach {
		t.Fatal("retained branch was not planned for reattach")
	}
	if _, err := fixture.executor().Execute(context.Background(), readd); err != nil {
		t.Fatalf("Execute reattach: %v", err)
	}
	manifest = fixture.loadManifest(t)
	if manifest.Revision != 3 || len(manifest.Repositories) != 2 || len(manifest.InactiveRepositories) != 0 {
		t.Fatalf("manifest after reattach = %+v", manifest)
	}
}

func TestRemoveRejectsDirtyAndLastRepository(t *testing.T) {
	fixture := newFixture(t)
	planner := Planner{Git: SystemGit{}}
	if _, err := planner.BuildRemove(context.Background(), fixture.catalog("api"), RemoveRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"api"},
	}); err == nil || !strings.Contains(err.Error(), "last repository") {
		t.Fatalf("last repository error = %v", err)
	}

	add, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.executor().Execute(context.Background(), add); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workRoot, "web", "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := planner.BuildRemove(context.Background(), fixture.catalog("web"), RemoveRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"},
	}); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty repository error = %v", err)
	}
}

func TestPlannerRejectsGoWorkDrift(t *testing.T) {
	fixture := newFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.workRoot, "go.work"), []byte("go 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (Planner{Git: SystemGit{}}).BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err == nil || !strings.Contains(err.Error(), "go.work does not match") {
		t.Fatalf("go.work drift error = %v", err)
	}
}

func TestExecutorNeverRemovesWorktreeThatBecameDirtyAfterPlanning(t *testing.T) {
	fixture := newFixture(t)
	planner := Planner{Git: SystemGit{}}
	add, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.executor().Execute(context.Background(), add); err != nil {
		t.Fatal(err)
	}
	remove, err := planner.BuildRemove(context.Background(), fixture.catalog("web"), RemoveRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := remove.Repositories[0].Destination
	if err := os.WriteFile(filepath.Join(destination, "late-dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.executor().Execute(context.Background(), remove); err == nil {
		t.Fatal("dirty worktree was removed after planning")
	}
	if info, err := os.Stat(destination); err != nil || !info.IsDir() {
		t.Fatalf("dirty worktree destination was not preserved: info=%v err=%v", info, err)
	}
}

func TestOnlineAddFetchesBeforePinningBase(t *testing.T) {
	fixture := newFixture(t)
	gitRun(t, fixture.web, "remote", "add", "origin", fixture.web)
	planner := Planner{
		Git: SystemGit{},
		Locker: newwork.FileRepositoryLocker{
			Set: lockops.Set{Root: fixture.controlRoot},
		},
	}
	plan, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOnline,
	})
	if err != nil {
		t.Fatalf("BuildAdd online: %v", err)
	}
	if plan.Repositories[0].FetchedAt == nil || plan.Repositories[0].BaseOID == "" {
		t.Fatalf("online plan did not record fetch and immutable base: %+v", plan.Repositories[0])
	}
}

func TestAddRejectsForeignAndChangedRetainedBranches(t *testing.T) {
	t.Run("foreign", func(t *testing.T) {
		fixture := newFixture(t)
		gitRun(t, fixture.web, "branch", fixture.name.String(), "main")
		_, err := (Planner{Git: SystemGit{}}).BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
			WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
		})
		if err == nil || !strings.Contains(err.Error(), "unowned Work branch") {
			t.Fatalf("foreign branch error = %v", err)
		}
	})

	t.Run("retained branch moved", func(t *testing.T) {
		fixture := newFixture(t)
		planner := Planner{Git: SystemGit{}}
		add, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
			WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.executor().Execute(context.Background(), add); err != nil {
			t.Fatal(err)
		}
		remove, err := planner.BuildRemove(context.Background(), fixture.catalog("web"), RemoveRequest{
			WorkName: fixture.name.String(), RepositoryIDs: []string{"web"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.executor().Execute(context.Background(), remove); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.web, "moved.txt"), []byte("moved\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, fixture.web, "add", "moved.txt")
		gitRun(t, fixture.web, "commit", "-m", "move retained branch target")
		newOID := strings.TrimSpace(gitOutput(t, fixture.web, "rev-parse", "HEAD"))
		gitRun(t, fixture.web, "update-ref", "refs/heads/"+fixture.name.String(), newOID)
		_, err = planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
			WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
		})
		if err == nil || !strings.Contains(err.Error(), "unowned Work branch") {
			t.Fatalf("moved retained branch error = %v", err)
		}
	})
}

func TestExplicitHarnessPathsSurviveRepositoryChanges(t *testing.T) {
	fixture := newFixture(t)
	toolsPath := filepath.Join(fixture.workRoot, "tools")
	if err := os.Mkdir(toolsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsPath, "go.mod"), []byte("module example.com/tools\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := fixture.loadManifest(t)
	manifest.Harness.RootModulesOnly = false
	manifest.Harness.UsePaths = []string{"./tools", "./api"}
	if err := work.ReplaceJSON(filepath.Join(fixture.workRoot, ".goworktree.json"), manifest, 0o644, nil); err != nil {
		t.Fatal(err)
	}
	content, exists, err := RenderManifestHarness(manifest, fixture.workRoot)
	if err != nil || !exists {
		t.Fatalf("render explicit harness: exists=%v err=%v", exists, err)
	}
	if err := work.ReplaceFile(filepath.Join(fixture.workRoot, "go.work"), content, 0o644, nil); err != nil {
		t.Fatal(err)
	}

	planner := Planner{Git: SystemGit{}}
	add, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(add.After.Harness.UsePaths, []string{"./tools", "./api", "./web"}) {
		t.Fatalf("add use paths = %v", add.After.Harness.UsePaths)
	}
	if _, err := fixture.executor().Execute(context.Background(), add); err != nil {
		t.Fatal(err)
	}
	remove, err := planner.BuildRemove(context.Background(), fixture.catalog("web"), RemoveRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(remove.After.Harness.UsePaths, []string{"./tools", "./api"}) {
		t.Fatalf("remove use paths = %v", remove.After.Harness.UsePaths)
	}
	if _, err := fixture.executor().Execute(context.Background(), remove); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.workRoot, "go.work"))
	if err != nil || strings.Contains(string(data), "./web") || !strings.Contains(string(data), "./tools") {
		t.Fatalf("final go.work = %q, %v", data, err)
	}
}

func TestRemovingLastExplicitRepositoryPathDoesNotEnableRootModuleDiscovery(t *testing.T) {
	fixture := newFixture(t)
	manifest := fixture.loadManifest(t)
	manifest.Harness.RootModulesOnly = false
	manifest.Harness.UsePaths = []string{"./api"}
	removeHarnessRepository(&manifest, manifest.Repositories[0])
	if manifest.Harness.RootModulesOnly || len(manifest.Harness.UsePaths) != 0 {
		t.Fatalf("empty explicit intent was changed to root discovery: %+v", manifest.Harness)
	}
	content, exists, err := RenderManifestHarness(manifest, fixture.workRoot)
	if err != nil || exists || len(content) != 0 {
		t.Fatalf("empty explicit harness = %q, exists=%v, err=%v", content, exists, err)
	}
}

func TestPartialBatchResumesFromRepositoryCheckpoints(t *testing.T) {
	fixture := newFixture(t)
	planner := Planner{Git: SystemGit{}}
	plan, err := planner.BuildAdd(context.Background(), fixture.catalog("web", "worker"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web", "worker"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := fixture.executor()
	executor.Git = &failSecondCreateGit{SystemGit: SystemGit{}}
	result, err := executor.Execute(context.Background(), plan)
	if err == nil || len(result.Repositories) != 2 || result.Repositories[0].Status != "completed" || result.Repositories[1].Err == nil {
		t.Fatalf("partial result = %+v, err=%v", result, err)
	}
	record, err := LoadRecord(plan.OperationPath)
	if err != nil || record.Repositories[0].State != StepDone || record.Repositories[1].State == StepDone {
		t.Fatalf("partial checkpoints = %+v, err=%v", record.Repositories, err)
	}
	if _, err := fixture.executor().Resume(context.Background(), plan.OperationPath); err != nil {
		t.Fatalf("Resume partial batch: %v", err)
	}
	manifest := fixture.loadManifest(t)
	if manifest.Revision != 1 || len(manifest.Repositories) != 3 {
		t.Fatalf("manifest after partial Resume = %+v", manifest)
	}
}

func TestResumeRejectsManifestChangedAfterConfirmation(t *testing.T) {
	fixture := newFixture(t)
	plan, err := (Planner{Git: SystemGit{}}).BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := fixture.executor()
	executor.Git = failCreateGit{SystemGit: SystemGit{}}
	if _, err := executor.Execute(context.Background(), plan); err == nil {
		t.Fatal("injected create failure did not interrupt operation")
	}
	manifest := fixture.loadManifest(t)
	manifest.CreatedAt = manifest.CreatedAt.Add(time.Second)
	if err := work.ReplaceJSON(filepath.Join(fixture.workRoot, ".goworktree.json"), manifest, 0o644, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.executor().Resume(context.Background(), plan.OperationPath); err == nil ||
		!strings.Contains(err.Error(), "changed after the repository change was confirmed") {
		t.Fatalf("Resume manifest drift error = %v", err)
	}
}

type failSecondCreateGit struct {
	SystemGit
	calls int
}

type failCreateGit struct{ SystemGit }

func (failCreateGit) CreateWorktreeAtOID(context.Context, string, string, string, string) error {
	return errors.New("injected create failure")
}

func (g *failSecondCreateGit) CreateWorktreeAtOID(ctx context.Context, repo, destination, branchRef, oid string) error {
	g.calls++
	if g.calls == 2 {
		return errors.New("injected second create failure")
	}
	return g.SystemGit.CreateWorktreeAtOID(ctx, repo, destination, branchRef, oid)
}

func TestInterruptedBranchDeletionResumes(t *testing.T) {
	fixture := newFixture(t)
	planner := Planner{Git: SystemGit{}}
	add, err := planner.BuildAdd(context.Background(), fixture.catalog("web"), AddRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, Mode: newwork.ModeOffline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.executor().Execute(context.Background(), add); err != nil {
		t.Fatal(err)
	}
	remove, err := planner.BuildRemove(context.Background(), fixture.catalog("web"), RemoveRequest{
		WorkName: fixture.name.String(), RepositoryIDs: []string{"web"}, DeleteBranches: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	failing := fixture.executor()
	failing.Git = failDeleteGit{SystemGit: SystemGit{}}
	if _, err := failing.Execute(context.Background(), remove); err == nil {
		t.Fatal("branch deletion failure did not interrupt operation")
	}
	record, err := LoadRecord(remove.OperationPath)
	if err != nil || record.Phase != PhaseInterrupted || !record.Repositories[0].WorktreeRemoved {
		t.Fatalf("interrupted record = %+v, %v", record, err)
	}
	if record.Repositories[0].Error == "" {
		t.Fatal("interrupted repository checkpoint did not preserve its error")
	}
	if _, err := fixture.executor().Resume(context.Background(), remove.OperationPath); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, exists, err := gitops.LocalBranchOIDContext(context.Background(), fixture.web, remove.Repositories[0].BranchRef); err != nil || exists {
		t.Fatalf("branch after Resume exists = %v, %v", exists, err)
	}
	registrations, err := gitops.ListWorktreesContext(context.Background(), fixture.web)
	if err != nil {
		t.Fatal(err)
	}
	if hasPlannedRegistration(registrations, remove.Repositories[0]) {
		t.Fatalf("removed worktree remains registered: %+v", registrations)
	}
	manifest := fixture.loadManifest(t)
	if manifest.Revision != 2 || manifest.InactiveRepositories[0].BranchRetained {
		t.Fatalf("manifest after resumed delete = %+v", manifest)
	}
}

type failDeleteGit struct{ SystemGit }

func (failDeleteGit) DeleteLocalBranchAtOID(context.Context, string, string, string) error {
	return errors.New("injected branch deletion failure")
}

type changeFixture struct {
	t           *testing.T
	root        string
	worksRoot   string
	controlRoot string
	workRoot    string
	api         string
	web         string
	worker      string
	name        work.Name
	manifest    work.Manifest
}

func newFixture(t *testing.T) *changeFixture {
	t.Helper()
	root := t.TempDir()
	worksRoot := filepath.Join(root, "works")
	controlRoot := filepath.Join(root, "control")
	if err := os.MkdirAll(worksRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	api := initRepo(t, filepath.Join(root, "api"), "module example.com/api\n\ngo 1.23\n")
	web := initRepo(t, filepath.Join(root, "web"), "module example.com/web\n\ngo 1.24\n")
	worker := initRepo(t, filepath.Join(root, "worker"), "module example.com/worker\n\ngo 1.23\n")
	name, _ := work.ParseName("ticket-42")
	workRoot := filepath.Join(worksRoot, name.String())
	if err := os.Mkdir(workRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, api, "worktree", "add", "-b", name.String(), filepath.Join(workRoot, "api"), "main")
	identity, err := gitops.InspectRepositoryContext(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}
	baseOID := strings.TrimSpace(gitOutput(t, api, "rev-parse", "main"))
	manifest := work.Manifest{
		SchemaVersion: work.LegacyManifestSchemaVersion,
		WorkID:        work.NewIdentity(worksRoot, name), NewWorkOperationID: strings.Repeat("a", 32),
		Name: name, CreatedAt: time.Now().UTC(),
		Repositories: []work.RepositoryIntent{{
			ID: "api", SourcePath: identity.SourcePath, GitCommonDir: identity.CommonDir,
			BaseRef: "refs/heads/main", BaseOID: baseOID, BranchRef: "refs/heads/" + name.String(),
			Destination: filepath.Join(workRoot, "api"), IncludeInGoWork: true,
		}},
		Harness: work.HarnessIntent{Kind: "go.work", RootModulesOnly: true, RepositoryIDs: []string{"api"}},
	}
	if err := work.CreateJSON(filepath.Join(workRoot, ".goworktree.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	content, exists, err := RenderManifestHarness(manifest, workRoot)
	if err != nil || !exists {
		t.Fatalf("render initial harness = %v, %v", exists, err)
	}
	if err := work.CreateFile(filepath.Join(workRoot, "go.work"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return &changeFixture{
		t: t, root: root, worksRoot: worksRoot, controlRoot: controlRoot,
		workRoot: workRoot, api: api, web: web, worker: worker, name: name, manifest: manifest,
	}
}

func (f *changeFixture) catalog(ids ...string) Catalog {
	manifest := f.loadManifest(f.t)
	repositories := make(map[string]Repository, len(ids))
	for _, id := range ids {
		path := f.api
		switch id {
		case "web":
			path = f.web
		case "worker":
			path = f.worker
		}
		repositories[id] = Repository{
			ID: id, SourcePath: path, Folder: id, Remote: "origin", BasePreference: "main",
		}
	}
	return Catalog{WorkRoot: f.workRoot, ControlRoot: f.controlRoot, Manifest: manifest, Repositories: repositories}
}

func (f *changeFixture) executor() Executor {
	return Executor{Git: SystemGit{}, Locker: FileLocker{Set: lockops.Set{Root: f.controlRoot}}}
}

func (f *changeFixture) loadManifest(t *testing.T) work.Manifest {
	t.Helper()
	var manifest work.Manifest
	if err := work.LoadJSON(filepath.Join(f.workRoot, ".goworktree.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func initRepo(t *testing.T, path, goMod string) string {
	t.Helper()
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, path, "init", "-b", "main")
	gitRun(t, path, "config", "user.email", "test@example.com")
	gitRun(t, path, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(path, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, path, "add", ".")
	gitRun(t, path, "commit", "-m", "initial")
	return path
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
