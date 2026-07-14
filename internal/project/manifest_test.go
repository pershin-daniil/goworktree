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
