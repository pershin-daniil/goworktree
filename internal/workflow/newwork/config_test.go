package newwork

import (
	"testing"

	"github.com/pershin-daniil/goworktree/internal/config"
)

func TestCatalogFromConfigPreservesRepositoryIDsAndRemote(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ProjectsRoot:  "/works",
		DefaultBranch: "main",
		Repos: map[string]config.Repo{
			"group/api": {Path: "/repos/api", Alias: "api", Remote: "upstream", DefaultBranch: "develop"},
		},
	}
	catalog, err := CatalogFromConfig(cfg, []string{"group/api"})
	if err != nil {
		t.Fatal(err)
	}
	got := catalog.Repositories["group/api"]
	if got.ID != "group/api" || got.Folder != "api" || got.Remote != "upstream" || got.BasePreference != "develop" {
		t.Fatalf("repository = %+v", got)
	}
}

func TestCatalogFromConfigDefaultsRemoteToOrigin(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		ProjectsRoot:  "/works",
		DefaultBranch: "main",
		Repos: map[string]config.Repo{
			"api": {Path: "/repos/api"},
		},
	}
	catalog, err := CatalogFromConfig(cfg, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.Repositories["api"].Remote; got != "origin" {
		t.Fatalf("remote = %q, want origin", got)
	}
}
