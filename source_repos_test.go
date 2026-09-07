package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pershin-daniil/goworktree/internal/config"
)

func TestSourceRepositoryScopeSelectsGroupAndSortsIDs(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.Repo{
		"web":    {Groups: []string{"team-a"}},
		"api":    {Groups: []string{"team-a", "platform"}},
		"worker": {Groups: []string{"team-b"}},
	}}
	ids, err := sourceRepositoryScope(cfg, []string{"--group", "team-a"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api", "web"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestSourceRepositoryScopeRequiresExplicitNetworkScope(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.Repo{"api": {}}}
	if _, err := sourceRepositoryScope(cfg, nil, true); err == nil || !strings.Contains(err.Error(), "--repos") {
		t.Fatalf("error = %v", err)
	}
	if _, err := sourceRepositoryScope(cfg, []string{"--repos", "api", "--all"}, true); err == nil {
		t.Fatal("mixed scopes were accepted")
	}
	if _, err := sourceRepositoryScope(cfg, []string{"--bogus"}, false); err == nil {
		t.Fatal("unknown scope argument was accepted")
	}
}

func TestRequirePathWithinReposRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "team", "api")
	if err := requirePathWithinReposRoot(root, inside); err != nil {
		t.Fatalf("inside path rejected: %v", err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := requirePathWithinReposRoot(root, outside); err == nil {
		t.Fatal("outside path accepted")
	}
}

func TestConfiguredSourceRepositoryConfigsIncludeAllConfiguredRepositories(t *testing.T) {
	reposRoot := t.TempDir()
	outsidePath := filepath.Join(filepath.Dir(reposRoot), "outside", "api")
	cfg := &config.Config{
		ReposRoot:     reposRoot,
		DefaultBranch: "main",
		Repos: map[string]config.Repo{
			"api":     {Path: outsidePath, Alias: "API", Groups: []string{"team-a"}},
			"missing": {},
		},
	}

	configured, err := configuredSourceRepositoryConfigs(cfg, cfg.RepoNames())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(configured), 2; got != want {
		t.Fatalf("configured repository count = %d, want %d", got, want)
	}
	if got, want := configured[0].ID, "api"; got != want {
		t.Fatalf("first repository ID = %q, want %q", got, want)
	}
	if got := configured[0].Path; got != outsidePath {
		t.Fatalf("outside repository path = %q, want %q", got, outsidePath)
	}
	if got, want := configured[1].ID, "missing"; got != want {
		t.Fatalf("second repository ID = %q, want %q", got, want)
	}
	if got := configured[1].Path; got != "" {
		t.Fatalf("missing repository path = %q, want empty", got)
	}
}

func TestConfiguredSourceRepositoryConfigsRejectUnknownRepository(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.Repo{"api": {Path: "/repos/api"}}}
	if _, err := configuredSourceRepositoryConfigs(cfg, []string{"missing"}); err == nil {
		t.Fatal("unknown repository was accepted")
	}
}

func TestScanConfiguredSourceRepositoriesPreservesGroupsDuringIDMigration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	reposRoot := filepath.Join(home, "repos")
	repoPath := filepath.Join(reposRoot, "team", "api")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	cfg := config.Default()
	cfg.ReposRoot = reposRoot
	cfg.ScanDepth = 3
	cfg.Repos = map[string]config.Repo{
		"api": {Path: repoPath, DefaultBranch: "main", Groups: []string{"team-a"}},
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := scanConfiguredSourceRepositories(context.Background()); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	id := config.RepoIDFromPath(reposRoot, repoPath)
	if got := loaded.RepoGroups(id); !reflect.DeepEqual(got, []string{"team-a"}) {
		t.Fatalf("groups after scan = %v", got)
	}
}
