package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../outside", "nested/name", ".", "..", ""} {
		if _, err := SafeJoin(root, name); err == nil {
			t.Fatalf("SafeJoin(%q) accepted unsafe name", name)
		}
	}
	got, err := SafeJoin(root, "ticket-42")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "ticket-42") {
		t.Fatalf("SafeJoin = %q", got)
	}
}

func TestManifestRejectsUnsafeFolder(t *testing.T) {
	dir := t.TempDir()
	m := NewManifest("ticket", []ManifestRepo{{ID: "repo", Folder: "../outside"}})
	if err := m.Save(dir); err == nil {
		t.Fatal("Save accepted unsafe folder")
	}
	if _, err := os.Stat(ManifestPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("manifest exists: %v", err)
	}
}

func TestProjectLockIsExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()
	if _, err := AcquireLock(dir); err == nil {
		t.Fatal("second lock unexpectedly acquired")
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(dir); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
}
