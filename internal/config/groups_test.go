package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidGroupName(t *testing.T) {
	valid := []string{"team-a", "team_a", "team.a", "A1", "1-team"}
	for _, name := range valid {
		if !ValidGroupName(name) {
			t.Errorf("ValidGroupName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "-team", ".team", "_team", "team name", "team/one", "команда"}
	for _, name := range invalid {
		if ValidGroupName(name) {
			t.Errorf("ValidGroupName(%q) = true, want false", name)
		}
	}
}

func TestLoadNormalizesGroupsAndRejectsInvalidPersistedGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	data := []byte(`{"repos":{"api":{"groups":["zeta","Alpha","zeta","team-a"]}}}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Alpha", "team-a", "zeta"}
	if got := cfg.RepoGroups("api"); !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %#v, want %#v", got, want)
	}
	if err := os.WriteFile(path, []byte(`{"repos":{"api":{"groups":["bad group"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load()
	if err == nil || !strings.Contains(err.Error(), `repository "api"`) || !strings.Contains(err.Error(), `bad group`) {
		t.Fatalf("Load() error = %v, want contextual invalid group error", err)
	}
}

func TestSaveNormalizesAndValidatesGroups(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := Default()
	cfg.Repos = map[string]Repo{"api": {Groups: []string{"zeta", "Alpha", "zeta"}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loaded.RepoGroups("api"), []string{"Alpha", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved groups = %#v, want %#v", got, want)
	}
	cfg.Repos["api"] = Repo{Groups: []string{"invalid group"}}
	if err := cfg.Save(); err == nil || !strings.Contains(err.Error(), `repository "api"`) {
		t.Fatalf("Save() error = %v, want contextual invalid group error", err)
	}
}

func TestRepoGroupOperations(t *testing.T) {
	cfg := &Config{Repos: map[string]Repo{
		"api":  {Groups: []string{"team-a", "Alpha", "team-a"}},
		"web":  {Groups: []string{"team-a", "team-b"}},
		"misc": {},
	}}
	if err := cfg.AddRepoGroup([]string{"api", "misc"}, "team-b"); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.RepoGroups("api"), []string{"Alpha", "team-a", "team-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("api groups = %#v, want %#v", got, want)
	}
	if got, want := cfg.GroupNames(), []string{"Alpha", "team-a", "team-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupNames() = %#v, want %#v", got, want)
	}
	groups := cfg.RepoGroups("api")
	groups[0] = "changed"
	if got := cfg.RepoGroups("api")[0]; got != "Alpha" {
		t.Fatalf("RepoGroups returned an alias; stored first group = %q", got)
	}
	if err := cfg.RemoveRepoGroup([]string{"api", "misc"}, "team-b"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.RenameGroup("team-a", "platform"); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.RepoGroups("web"), []string{"platform", "team-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("web groups after rename = %#v, want %#v", got, want)
	}
	if err := cfg.DeleteGroup("team-b"); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.GroupNames(), []string{"Alpha", "platform"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups after delete = %#v, want %#v", got, want)
	}
}

func TestRepoGroupOperationsAreAtomicForUnknownRepos(t *testing.T) {
	cfg := &Config{Repos: map[string]Repo{"api": {Groups: []string{"team-a"}}}}
	before := cfg.RepoGroups("api")
	if err := cfg.AddRepoGroup([]string{"api", "missing"}, "team-b"); err == nil {
		t.Fatal("AddRepoGroup accepted an unknown repository")
	}
	if got := cfg.RepoGroups("api"); !reflect.DeepEqual(got, before) {
		t.Fatalf("AddRepoGroup partially changed api groups: %#v", got)
	}
	if err := cfg.RemoveRepoGroup([]string{"api", "missing"}, "team-a"); err == nil {
		t.Fatal("RemoveRepoGroup accepted an unknown repository")
	}
	if got := cfg.RepoGroups("api"); !reflect.DeepEqual(got, before) {
		t.Fatalf("RemoveRepoGroup partially changed api groups: %#v", got)
	}
}

func TestRenameAndDeleteMissingGroupFail(t *testing.T) {
	cfg := &Config{Repos: map[string]Repo{"api": {Groups: []string{"team-a"}}}}
	if err := cfg.RenameGroup("missing", "team-b"); err == nil {
		t.Fatal("RenameGroup accepted a missing group")
	}
	if err := cfg.DeleteGroup("missing"); err == nil {
		t.Fatal("DeleteGroup accepted a missing group")
	}
}
