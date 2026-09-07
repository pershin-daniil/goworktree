package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pershin-daniil/goworktree/internal/config"
	gitops "github.com/pershin-daniil/goworktree/internal/git"
	"github.com/pershin-daniil/goworktree/internal/project"
	"github.com/pershin-daniil/goworktree/internal/work"
	"github.com/pershin-daniil/goworktree/internal/workflow/changework"
	"github.com/pershin-daniil/goworktree/internal/workflow/newwork"
)

func TestBranchAdoptionPreservesFilesAndWorksWithSyncDropAndReattach(t *testing.T) {
	cfg := commandWorkFixture(t)
	destination := filepath.Join(cfg.ProjectsRoot, "ticket", "api")
	commandGit(t, destination, "switch", "-c", "feature/adopted")
	dirty := filepath.Join(destination, "pending.txt")
	if err := os.WriteFile(dirty, []byte("keep these changes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runBranch([]string{"ticket", "api"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := inspectConfiguredWorks(context.Background())
	if err != nil || len(snapshot.Works) != 1 || snapshot.Works[0].Snapshot == nil {
		t.Fatalf("inspection after adoption: %+v, %v", snapshot, err)
	}
	observed := snapshot.Works[0].Snapshot
	if len(observed.Problems) != 0 || observed.Manifest.Value.Repositories[0].BranchRef != "refs/heads/feature/adopted" {
		t.Fatalf("adopted intent: %+v; problems: %+v", observed.Manifest, observed.Problems)
	}
	record, err := changework.LoadRecord(observed.ChangeOperation.Path)
	if err != nil || !changework.RecordMatchesManifest(record, *observed.Manifest.Value) {
		t.Fatalf("adoption provenance: %+v, %v", record, err)
	}
	if err := runSync([]string{"ticket"}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(dirty); err != nil || string(data) != "keep these changes\n" {
		t.Fatalf("local edits were changed: %q, %v", data, err)
	}
	commandGit(t, destination, "add", "pending.txt")
	commandGit(t, destination, "commit", "-m", "keep edits")
	if err := runDrop([]string{"ticket", "--repos", "api", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if err := runAdd([]string{"ticket", "--repos", "api", "--offline"}); err != nil {
		t.Fatalf("reattach adopted branch: %v", err)
	}
	if got := commandGit(t, destination, "symbolic-ref", "HEAD"); got != "refs/heads/feature/adopted" {
		t.Fatalf("reattached branch = %s", got)
	}
}

func TestDropRetryMustMatchRecordedScopeAndDeletionPolicy(t *testing.T) {
	cfg := commandWorkFixture(t)
	api := cfg.Repos["api"].Path
	lock := filepath.Join(api, ".git", "refs", "heads", "ticket.lock")
	if err := os.WriteFile(lock, []byte("injected lock failure\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original := []string{"ticket", "--repos", "api", "-D", "--yes"}
	if err := runDrop(original); err == nil {
		t.Fatal("expected injected branch deletion failure")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(cfg.ProjectsRoot, "ticket", ".goworktree.json")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"ticket", "--repos", "web", "-D", "--yes"},
		{"ticket", "--repos", "api", "--yes"},
	} {
		if err := runDrop(args); err == nil || !strings.Contains(err.Error(), "pending") {
			t.Fatalf("mismatched retry %v: %v", args, err)
		}
		if _, exists, err := gitops.LocalBranchOIDContext(context.Background(), api, "refs/heads/ticket"); err != nil || !exists {
			t.Fatalf("unrequested branch was deleted: exists=%v err=%v", exists, err)
		}
		if after, err := os.ReadFile(manifestPath); err != nil || string(before) != string(after) {
			t.Fatalf("rejected retry changed manifest: %v", err)
		}
	}
	if err := runDrop(original); err != nil {
		t.Fatalf("matching retry: %v", err)
	}
	if _, exists, err := gitops.LocalBranchOIDContext(context.Background(), api, "refs/heads/ticket"); err != nil || exists {
		t.Fatalf("matching retry did not delete branch: exists=%v err=%v", exists, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.ProjectsRoot, "ticket", "web")); err != nil {
		t.Fatalf("unselected worktree changed: %v", err)
	}
}

func TestBranchAdoptionRejectsCheckoutChangedAfterPlanning(t *testing.T) {
	cfg := commandWorkFixture(t)
	destination := filepath.Join(cfg.ProjectsRoot, "ticket", "api")
	commandGit(t, destination, "switch", "-c", "feature/first")
	_, catalog, err := configuredChangeCatalog(context.Background(), "ticket", []string{"api"}, changework.KindAdopt)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (changework.Planner{Git: changework.SystemGit{}}).BuildAdopt(context.Background(), catalog, changework.AdoptRequest{
		WorkName: "ticket", RepositoryID: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	commandGit(t, destination, "switch", "-c", "feature/second")
	if _, err := runConfiguredRepositoryChange(context.Background(), plan); err == nil {
		t.Fatal("stale branch plan was accepted")
	}
	var manifest work.Manifest
	if err := work.LoadJSON(filepath.Join(catalog.WorkRoot, ".goworktree.json"), &manifest); err != nil || manifest.Revision != 0 {
		t.Fatalf("stale plan changed intent: %+v, %v", manifest, err)
	}
	commandGit(t, destination, "switch", "feature/first")
	if err := runBranch([]string{"ticket", "api"}); err != nil {
		t.Fatalf("resume original branch adoption: %v", err)
	}
}

func TestResumeRequestComparesModeAndRepositorySet(t *testing.T) {
	plan := changework.Plan{WorkName: "ticket", Kind: changework.KindAdd, Mode: newwork.ModeOffline,
		Repositories: []changework.RepositoryPlan{{ID: "api"}, {ID: "web"}}}
	request := changework.ResumeRequest{WorkName: "ticket", Kind: changework.KindAdd, Mode: newwork.ModeOffline, RepositoryIDs: []string{"web", "api"}}
	if err := plan.MatchRequest(request); err != nil {
		t.Fatal(err)
	}
	request.Mode = newwork.ModeOnline
	if err := plan.MatchRequest(request); err == nil {
		t.Fatal("changed network mode accepted")
	}
}

func TestBranchAdoptionRejectsDetachedHead(t *testing.T) {
	cfg := commandWorkFixture(t)
	destination := filepath.Join(cfg.ProjectsRoot, "ticket", "api")
	commandGit(t, destination, "switch", "--detach")
	if err := runBranch([]string{"ticket", "api"}); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("detached adoption: %v", err)
	}
}

func TestBranchKeepsLegacyManifestCompatibility(t *testing.T) {
	cfg := commandWorkFixture(t)
	root := filepath.Join(cfg.ProjectsRoot, "ticket")
	legacy := project.NewManifest("ticket", []project.ManifestRepo{{
		ID: "api", Folder: "api", Path: cfg.Repos["api"].Path, Branch: "ticket", Base: "main", Status: project.StatusReady,
	}})
	if err := legacy.Save(root); err != nil {
		t.Fatal(err)
	}
	commandGit(t, filepath.Join(root, "api"), "switch", "-c", "feature/legacy")
	if err := runBranch([]string{"ticket", "api"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := project.LoadManifest(root)
	if err != nil || loaded.Repos[0].Branch != "feature/legacy" {
		t.Fatalf("legacy branch: %+v, %v", loaded, err)
	}
}

func TestRemoveArchivesRetainedBranchIntentAndAllowsNameReuse(t *testing.T) {
	cfg := commandWorkFixture(t)
	if err := runDrop([]string{"ticket", "--repos", "api", "--yes"}); err != nil {
		t.Fatal(err)
	}
	plan, err := planConfiguredRemoveWork(context.Background(), "ticket")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Repositories) != 2 || !plan.Repositories[0].BranchOnly {
		t.Fatalf("retained branch absent from confirmation: %+v", plan.Repositories)
	}
	result, err := runConfiguredRemoveWork(context.Background(), plan, "ticket")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.ArchivePath, "change-work.json")); err != nil {
		t.Fatalf("change provenance not archived: %v", err)
	}
	for _, id := range []string{"api", "web"} {
		if _, exists, err := gitops.LocalBranchOIDContext(context.Background(), cfg.Repos[id].Path, "refs/heads/ticket"); err != nil || exists {
			t.Fatalf("branch %s not removed: exists=%v err=%v", id, exists, err)
		}
	}
	if err := runStart([]string{"ticket", "--repos", "api", "--offline"}); err != nil {
		t.Fatalf("reuse name: %v", err)
	}
	if err := runAdd([]string{"ticket", "--repos", "web", "--offline"}); err != nil {
		t.Fatalf("change newly recreated Work: %v", err)
	}
	if err := runDrop([]string{"ticket", "--repos", "web", "--yes"}); err != nil {
		t.Fatalf("drop from recreated Work: %v", err)
	}
}

func commandWorkFixture(t *testing.T) *config.Config {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	cfg := config.Default()
	cfg.ReposRoot = filepath.Join(root, "repos")
	cfg.ProjectsRoot = filepath.Join(root, "works")
	if err := os.MkdirAll(cfg.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"api", "web"} {
		source := filepath.Join(cfg.ReposRoot, id)
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatal(err)
		}
		commandGit(t, source, "init", "-b", "main")
		commandGit(t, source, "config", "user.name", "Test")
		commandGit(t, source, "config", "user.email", "test@example.com")
		if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/"+id+"\n\ngo 1.23\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commandGit(t, source, "add", ".")
		commandGit(t, source, "commit", "-m", "initial")
		commandGit(t, source, "remote", "add", "origin", source)
		cfg.Repos[id] = config.Repo{Path: source, Alias: id, DefaultBranch: "main"}
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := runStart([]string{"ticket", "--repos", "api,web", "--offline"}); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func commandGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
