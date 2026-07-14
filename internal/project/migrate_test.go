package project

import (
	"path/filepath"
	"testing"

	"github.com/pershin-daniil/goworktree/internal/config"
)

func TestMigrateManifestUsesFolderNames(t *testing.T) {
	// Ensure ManifestRepo.Folder from migrate is the directory name, not alias rewrite.
	// Full git smoke lives in git_test; here we only check FindRepoID wiring via config.
	cfg := config.Default()
	cfg.ReposRoot = "/repos"
	cfg.Repos = map[string]config.Repo{
		"cloud-vpc-vpc-controller": {
			Path:          "/repos/cloud/vpc/vpc-controller",
			DefaultBranch: "develop",
			Alias:         "vpc-controller",
		},
	}
	id, ok := cfg.FindRepoID("/repos/cloud/vpc/vpc-controller")
	if !ok || id != "cloud-vpc-vpc-controller" {
		t.Fatalf("FindRepoID = %q ok=%v", id, ok)
	}
	id, ok = cfg.FindRepoID(filepath.Join("/repos", "cloud", "vpc", "vpc-controller"))
	if !ok || id != "cloud-vpc-vpc-controller" {
		t.Fatalf("FindRepoID cleaned = %q ok=%v", id, ok)
	}
}
