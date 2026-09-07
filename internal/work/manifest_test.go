package work

import (
	"strings"
	"testing"
	"time"
)

func TestManifestValidatesCustomHarnessUsePaths(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest()
	manifest.Harness = HarnessIntent{
		Kind: "go.work", RootModulesOnly: false, RepositoryIDs: []string{"api"},
		UsePaths: []string{"./api", "./controller/pkg/private"},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("custom harness rejected: %v", err)
	}

	for _, usePath := range []string{"/absolute", "../outside", "./api/../../outside", "./api/../api"} {
		invalid := manifest
		invalid.Harness.UsePaths = []string{usePath}
		if err := invalid.Validate(); err == nil {
			t.Fatalf("unsafe use path %q was accepted", usePath)
		}
	}

	invalid := manifest
	invalid.Harness.UsePaths = []string{"./api", "./api"}
	if err := invalid.Validate(); err == nil {
		t.Fatal("duplicate use path was accepted")
	}
}

func TestManifestRootModulesOnlyRejectsExplicitPaths(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest()
	manifest.Harness.UsePaths = []string{"./api"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("root-modules-only harness accepted explicit paths")
	}
}

func TestManifestAcceptsLegacyAndValidatesInactiveRepositoryOwnership(t *testing.T) {
	t.Parallel()

	legacy := validManifestForTest()
	legacy.SchemaVersion = LegacyManifestSchemaVersion
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy manifest rejected: %v", err)
	}

	changed := validManifestForTest()
	changed.Revision = 1
	changed.LastChangeID = strings.Repeat("b", 32)
	inactive := changed.Repositories[0]
	inactive.ID = "web"
	inactive.SourcePath = "/repos/web"
	inactive.GitCommonDir = "/repos/web/.git"
	inactive.Destination = "/works/work-1/web"
	inactive.IncludeInGoWork = false
	changed.InactiveRepositories = []InactiveRepositoryIntent{{
		Repository: inactive, BranchRetained: true, BranchOID: strings.Repeat("c", 40),
	}}
	if err := changed.Validate(); err != nil {
		t.Fatalf("schema-2 inactive repository rejected: %v", err)
	}

	changed.InactiveRepositories[0].Repository.ID = "api"
	if err := changed.Validate(); err == nil {
		t.Fatal("duplicate active/inactive repository ID was accepted")
	}
}

func validManifestForTest() Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion, WorkID: Identity("work-id"),
		NewWorkOperationID: strings.Repeat("a", 32), Name: Name("work-1"), CreatedAt: time.Unix(1, 0).UTC(),
		Repositories: []RepositoryIntent{{
			ID: "api", SourcePath: "/repos/api", GitCommonDir: "/repos/api/.git",
			BaseRef: "refs/heads/main", BaseOID: "base", BranchRef: "refs/heads/work-1",
			Destination: "/works/work-1/api", IncludeInGoWork: true,
		}},
		Harness: HarnessIntent{Kind: "go.work", RootModulesOnly: true, RepositoryIDs: []string{"api"}},
	}
}
