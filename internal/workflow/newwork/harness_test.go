package newwork

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pershin-daniil/goworktree/internal/work"
)

func TestParseGoVersionOrderingAndCanonicalForm(t *testing.T) {
	t.Parallel()

	v123, err := parseGoVersion("1.23")
	if err != nil {
		t.Fatal(err)
	}
	v1231, err := parseGoVersion("1.23.1")
	if err != nil {
		t.Fatal(err)
	}
	v124, err := parseGoVersion("1.24.0")
	if err != nil {
		t.Fatal(err)
	}
	if v1231.compare(v123) <= 0 || v124.compare(v1231) <= 0 {
		t.Fatalf("version ordering is wrong: %v %v %v", v123, v1231, v124)
	}
	if got := v124.String(); got != "1.24" {
		t.Fatalf("canonical version = %q, want 1.24", got)
	}
	for _, invalid := range []string{"", "1", "2.0", "1.x", "1.2.3.4", "1.23rc1", "1.023"} {
		if _, err := parseGoVersion(invalid); err == nil {
			t.Fatalf("parseGoVersion(%q) succeeded", invalid)
		}
	}
}

func TestStripGoModComments(t *testing.T) {
	t.Parallel()

	inBlock := false
	if got := stripGoModComments("/* before", &inBlock); got != "" || !inBlock {
		t.Fatalf("block start = %q, %v", got, inBlock)
	}
	if got := stripGoModComments("after */ go 1.24 // trailing", &inBlock); got != " go 1.24 " || inBlock {
		t.Fatalf("block end = %q, %v", got, inBlock)
	}
}

func TestExpectedGoWorkPreservesExplicitNestedModules(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for path, content := range map[string]string{
		"vpc-api/go.mod":        "module example.test/api\n\ngo 1.24\n",
		"vpc-controller/go.mod": "module example.test/controller\n\ngo 1.26.5\n",
		"vpc-controller/pkg/cloudru/network/vpc/private/go.mod": "module example.test/private\n\ngo 1.25\n",
	} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan := Plan{WorkRoot: root, HarnessUsePaths: []string{
		"./vpc-api",
		"./vpc-controller",
		"./vpc-controller/pkg/cloudru/network/vpc/private",
	}}

	content, checkpoint, err := expectedGoWork(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := "go 1.26.5\n\nuse (\n\t./vpc-api\n\t./vpc-controller\n\t./vpc-controller/pkg/cloudru/network/vpc/private\n)\n"
	if string(content) != want {
		t.Fatalf("go.work:\n%s\nwant:\n%s", content, want)
	}
	if checkpoint.GoVersion != "1.26.5" || len(checkpoint.UsePaths) != 3 {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
}

func TestExpectedGoWorkQuotesSpecialUsePathsForGoParser(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}

	root := t.TempDir()
	paths := []string{"./simple", "./with space", "./модуль", "./with+plus"}
	if runtime.GOOS != "windows" {
		paths = append(paths, "./with\"quote", "./with\\backslash")
	}
	for index, usePath := range paths {
		directory := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(usePath, "./")))
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte(fmt.Sprintf("module example.test/module%d\n\ngo 1.23\n", index)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	content, _, err := expectedGoWork(Plan{WorkRoot: root, HarnessUsePaths: paths})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(content); !strings.Contains(got, "\t./simple\n") ||
		!strings.Contains(got, "\t\"./with space\"\n") ||
		!strings.Contains(got, "\t./модуль\n") ||
		!strings.Contains(got, "\t./with+plus\n") {
		t.Fatalf("unexpected go.work content:\n%s", content)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("go", "work", "edit", "-json")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go work edit -json: %v\n%s", err, out)
	}
	var parsed struct {
		Use []struct {
			DiskPath string
		}
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse go work edit output: %v\n%s", err, out)
	}
	got := make([]string, 0, len(parsed.Use))
	for _, use := range parsed.Use {
		got = append(got, use.DiskPath)
	}
	if !slices.Equal(got, paths) {
		t.Fatalf("parsed use paths = %#v, want %#v", got, paths)
	}
}

func TestExpectedGoWorkRejectsUnsafeExplicitPath(t *testing.T) {
	t.Parallel()

	_, _, err := expectedGoWork(Plan{WorkRoot: t.TempDir(), HarnessUsePaths: []string{"./repo/../../outside"}})
	if err == nil {
		t.Fatal("unsafe harness path was accepted")
	}
}

func TestExpectedGoWorkRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()

	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "go.mod"), []byte("module example.test/outside\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	_, _, err := expectedGoWork(Plan{WorkRoot: root, HarnessUsePaths: []string{"./outside"}})
	if err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestManifestAndPlanRoundTripExplicitHarnessPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	operationPath := filepath.Join(t.TempDir(), "operations", "new-work", strings.Repeat("b", 64)+".json")
	record := OperationRecord{
		OperationID: strings.Repeat("a", 32), WorkID: work.Identity(strings.Repeat("b", 64)),
		CreatedAt: time.Unix(1, 0).UTC(),
		Plan: Plan{
			WorkName: work.Name("work-1"), WorkID: work.Identity(strings.Repeat("b", 64)), WorkRoot: root,
			ManifestPath: filepath.Join(root, ".goworktree.json"), OperationRecordPath: operationPath,
			Repositories: []RepositoryPlan{{
				ID: "api", SourcePath: "/repos/api", GitCommonDir: "/repos/api/.git",
				BaseRef: "refs/heads/main", BaseOID: "base", TargetBranchRef: "refs/heads/work-1",
				Destination: filepath.Join(root, "api"), IncludeInGoWork: true,
			}},
			HarnessUsePaths: []string{"./api", "./api/pkg/private"}, NoRemoteMutation: true,
		},
	}
	manifest := manifestFromOperation(record)
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if manifest.Harness.RootModulesOnly || !slices.Equal(manifest.Harness.UsePaths, record.Plan.HarnessUsePaths) {
		t.Fatalf("manifest harness = %+v", manifest.Harness)
	}
	roundTrip, err := PlanFromManifest(manifest, root, operationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(roundTrip.HarnessUsePaths, record.Plan.HarnessUsePaths) {
		t.Fatalf("round-trip paths = %v", roundTrip.HarnessUsePaths)
	}
}
