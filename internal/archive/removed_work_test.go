package archive_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pershin-daniil/goworktree/internal/archive"
)

var archiveTime = time.Date(2026, time.August, 14, 12, 30, 45, 0, time.UTC)

func TestStorePublishListShowAndDelete(t *testing.T) {
	store := archive.Store{ControlRoot: t.TempDir(), Now: func() time.Time { return archiveTime }}
	first := publishArchive(t, store, "alpha", "work-alpha", "remove-alpha", true)
	second := publishArchive(t, store, "beta", "work-beta", "remove-beta", false)

	listed, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("List() returned %d archives; want 2", len(listed))
	}
	if listed[0].Manifest.ArchiveID != first.Manifest.ArchiveID || listed[1].Manifest.ArchiveID != second.Manifest.ArchiveID {
		t.Fatalf("List() IDs = %q, %q; want publication order for equal timestamps", listed[0].Manifest.ArchiveID, listed[1].Manifest.ArchiveID)
	}
	shown, err := store.Show(first.Manifest.ArchiveID)
	if err != nil {
		t.Fatalf("Show() error = %v", err)
	}
	if shown.Path != first.Path || len(shown.Manifest.Files) != 3 {
		t.Fatalf("Show() = %#v; want published archive with three metadata files", shown)
	}

	if err := store.Delete(context.Background(), first.Manifest.ArchiveID, first.Manifest.ArchiveID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Show(first.Manifest.ArchiveID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Show(deleted) error = %v; want not exist", err)
	}
	if _, err := store.Show(second.Manifest.ArchiveID); err != nil {
		t.Fatalf("Show(unrelated archive) error = %v", err)
	}
}

func TestStoreDeleteRequiresExactConfirmation(t *testing.T) {
	store := archive.Store{ControlRoot: t.TempDir(), Now: func() time.Time { return archiveTime }}
	summary := publishArchive(t, store, "alpha", "work-alpha", "remove-alpha", false)

	err := store.Delete(context.Background(), summary.Manifest.ArchiveID, "alpha")
	if err == nil || !strings.Contains(err.Error(), "confirmation must exactly equal") {
		t.Fatalf("Delete() error = %v; want exact-confirmation error", err)
	}
	if _, err := store.Show(summary.Manifest.ArchiveID); err != nil {
		t.Fatalf("archive was deleted after invalid confirmation: %v", err)
	}
}

func TestStoreNewIDRetriesCollisionsAndStopsAfterSixteen(t *testing.T) {
	control := t.TempDir()
	store := archive.Store{ControlRoot: control, Now: func() time.Time { return archiveTime }}
	root := store.Root()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	firstSalt := "\x01\x02\x03\x04"
	firstID := "alpha-20260814T123045Z-01020304"
	if err := os.Mkdir(filepath.Join(root, firstID), 0o700); err != nil {
		t.Fatal(err)
	}
	reads := []string{firstSalt, "\x05\x06\x07\x08"}
	store.Rand = func(p []byte) (int, error) {
		copy(p, reads[0])
		reads = reads[1:]
		return len(p), nil
	}
	id, err := store.NewID("alpha")
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if id != "alpha-20260814T123045Z-05060708" {
		t.Fatalf("NewID() = %q", id)
	}

	store.Rand = func(p []byte) (int, error) {
		copy(p, firstSalt)
		return len(p), nil
	}
	_, err = store.NewID("alpha")
	if err == nil || !strings.Contains(err.Error(), "after 16 attempts") {
		t.Fatalf("NewID() collision error = %v; want exhaustion", err)
	}
}

func TestStoreRejectsSymlinksTraversalAndCorruptArchives(t *testing.T) {
	store := archive.Store{ControlRoot: t.TempDir(), Now: func() time.Time { return archiveTime }}
	summary := publishArchive(t, store, "alpha", "work-alpha", "remove-alpha", false)

	if _, err := store.Show("../" + summary.Manifest.ArchiveID); err == nil {
		t.Fatal("Show() accepted traversal archive ID")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(summary.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, summary.Path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Show(summary.Manifest.ArchiveID); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("Show() symlink error = %v; want rejection", err)
	}

	corrupt := publishArchive(t, store, "beta", "work-beta", "remove-beta", false)
	if err := os.WriteFile(filepath.Join(corrupt.Path, "new-work.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Show(corrupt.Manifest.ArchiveID); err == nil || !strings.Contains(err.Error(), "digest verification") {
		t.Fatalf("Show() corrupt digest error = %v; want rejection", err)
	}
	if err := os.WriteFile(filepath.Join(corrupt.Path, "archive.json"), []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Show(corrupt.Manifest.ArchiveID); err == nil || !strings.Contains(err.Error(), "archive manifest") {
		t.Fatalf("Show() corrupt manifest error = %v; want rejection", err)
	}
}

func TestStoreDeleteResumesMarkedDeletion(t *testing.T) {
	store := archive.Store{ControlRoot: t.TempDir(), Now: func() time.Time { return archiveTime }}
	summary := publishArchive(t, store, "alpha", "work-alpha", "remove-alpha", false)
	deleting := filepath.Join(store.Root(), ".deleting-"+summary.Manifest.ArchiveID)
	if err := os.Rename(summary.Path, deleting); err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(context.Background(), summary.Manifest.ArchiveID, summary.Manifest.ArchiveID); err != nil {
		t.Fatalf("Delete(resume) error = %v", err)
	}
	if _, err := os.Lstat(deleting); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleting directory remains: %v", err)
	}
}

func publishArchive(t *testing.T, store archive.Store, workName, workID, removalID string, includeSync bool) archive.Summary {
	t.Helper()
	sources := t.TempDir()
	newRecord := writeRecord(t, sources, "new.json", "new")
	removeRecord := writeRecord(t, sources, "remove.json", "remove")
	syncRecord := ""
	if includeSync {
		syncRecord = writeRecord(t, sources, "sync.json", "sync")
	}
	id, err := store.NewID(workName)
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	summary, err := store.Publish(archive.PublishRequest{
		ArchiveID: id, WorkName: workName, WorkID: workID, RemovalID: removalID, CreatedAt: archiveTime,
		NewWorkRecord: newRecord, SyncWorkRecord: syncRecord, RemoveWorkRecord: removeRecord,
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	return summary
}

func writeRecord(t *testing.T, dir, name, value string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"record":"`+value+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
