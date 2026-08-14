package work

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndReplaceJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	initial := map[string]string{"id": "one"}
	if err := CreateJSON(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CreateJSON(path, initial, 0o600); err == nil {
		t.Fatal("CreateJSON overwrote existing file")
	}
	updated := map[string]string{"id": "one", "phase": "verified"}
	err := ReplaceJSON(path, updated, 0o600, func(current []byte) error {
		var got map[string]string
		if err := LoadJSON(path, &got); err != nil {
			return err
		}
		if got["id"] != "one" {
			return errors.New("ownership changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := LoadJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got["phase"] != "verified" {
		t.Fatalf("updated JSON = %#v", got)
	}
}

func TestReplaceJSONRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceJSON(link, map[string]string{"changed": "yes"}, 0o600, nil); err == nil {
		t.Fatal("ReplaceJSON accepted symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}\n" {
		t.Fatalf("symlink target changed: %q", data)
	}
}

func TestCleanupAtomicTempsUsesExactRegularFilePrefix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "state.json")
	temp := filepath.Join(root, ".state.json-crash")
	unrelated := filepath.Join(root, ".other.json-crash")
	for _, path := range []string{temp, unrelated} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupAtomicTemps(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned temporary file remains: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated file was removed: %v", err)
	}
}
