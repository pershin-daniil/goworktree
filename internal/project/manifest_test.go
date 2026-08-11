package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestRoundtripAndNeedsWork(t *testing.T) {
	dir := t.TempDir()
	m := NewManifest("ticket", []ManifestRepo{
		{ID: "a", Folder: "a", Path: "/r/a", Branch: "ticket", Base: "main", Status: StatusReady},
		{ID: "b", Folder: "b", Path: "/r/b", Branch: "ticket", Base: "develop", Status: StatusPending},
		{ID: "c", Folder: "c", Path: "/r/c", Branch: "ticket", Base: "main", Status: StatusFailed, Error: "boom"},
	})
	if err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "ticket" {
		t.Fatalf("name = %q", loaded.Name)
	}
	pending := loaded.NeedsWork()
	if len(pending) != 2 {
		t.Fatalf("NeedsWork len = %d", len(pending))
	}
	if loaded.ReadyCount() != 1 {
		t.Fatalf("ReadyCount = %d", loaded.ReadyCount())
	}

	loaded.SetStatus("b", StatusReady, "")
	if err := loaded.Save(dir); err != nil {
		t.Fatal(err)
	}
	again, _ := LoadManifest(dir)
	if again.ReadyCount() != 2 {
		t.Fatalf("after SetStatus ReadyCount = %d", again.ReadyCount())
	}
}

func TestContainsAddRemoveIDs(t *testing.T) {
	m := NewManifest("ticket", []ManifestRepo{
		{ID: "a", Folder: "a", Status: StatusReady},
	})
	if !m.Contains("a") || m.Contains("b") {
		t.Fatal("Contains failed")
	}
	m.AddRepo(ManifestRepo{ID: "b", Folder: "b", Status: StatusPending})
	m.AddRepo(ManifestRepo{ID: "b", Folder: "b2", Status: StatusPending}) // no-op duplicate
	if len(m.Repos) != 2 {
		t.Fatalf("len after AddRepo = %d", len(m.Repos))
	}
	ids := m.IDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("IDs = %v", ids)
	}
	removed, ok := m.RemoveRepo("a")
	if !ok || removed.ID != "a" {
		t.Fatalf("RemoveRepo = %+v ok=%v", removed, ok)
	}
	if m.Contains("a") || len(m.Repos) != 1 {
		t.Fatalf("after remove: contains=%v len=%d", m.Contains("a"), len(m.Repos))
	}
	if _, ok := m.RemoveRepo("missing"); ok {
		t.Fatal("expected missing remove to fail")
	}
}

func TestManifestRejectsAndRepairsDuplicateRepoPaths(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManifest("ticket", []ManifestRepo{
		{ID: "legacy", Folder: "repo", Path: repo, Status: StatusReady},
		{ID: "stable", Folder: "group-repo", Path: filepath.Join(dir, ".", "repo"), Status: StatusFailed},
	})
	if removed := m.DeduplicateRepos(); removed != 1 {
		t.Fatalf("DeduplicateRepos removed %d, want 1", removed)
	}
	if len(m.Repos) != 1 || m.Repos[0].ID != "legacy" || m.Repos[0].Status != StatusReady {
		t.Fatalf("DeduplicateRepos = %+v", m.Repos)
	}

	m.AddRepo(ManifestRepo{ID: "another-id", Path: repo, Status: StatusPending})
	if len(m.Repos) != 1 {
		t.Fatalf("AddRepo added duplicate path: %+v", m.Repos)
	}
}

func TestDeduplicateReposPrefersReadyEntry(t *testing.T) {
	m := NewManifest("ticket", []ManifestRepo{
		{ID: "failed", Path: "/repos/api", Status: StatusFailed},
		{ID: "ready", Path: "/repos/api", Status: StatusReady},
	})
	m.DeduplicateRepos()
	if len(m.Repos) != 1 || m.Repos[0].ID != "ready" {
		t.Fatalf("DeduplicateRepos = %+v", m.Repos)
	}
}

func TestSetBranch(t *testing.T) {
	m := NewManifest("ticket", []ManifestRepo{{ID: "api", Branch: "ticket"}})
	if !m.SetBranch("api", "team/ticket") || m.Repos[0].Branch != "team/ticket" {
		t.Fatalf("SetBranch = %+v", m.Repos)
	}
	if m.SetBranch("missing", "other") {
		t.Fatal("SetBranch unexpectedly updated a missing repository")
	}
}
