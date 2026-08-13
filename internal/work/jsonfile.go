package work

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CreateJSON atomically creates a new JSON file and fails if the target exists.
// The temporary file and target are hard links to the same fully synced inode,
// so a crash cannot expose partially written JSON at the target path.
func CreateJSON(path string, value any, perm os.FileMode) error {
	data, err := marshalJSON(value)
	if err != nil {
		return err
	}
	return CreateFile(path, data, perm)
}

// CreateFile atomically creates a fully synced regular file without replacing
// an existing path.
func CreateFile(path string, data []byte, perm os.FileMode) error {
	temp, err := writeTemp(filepath.Dir(path), filepath.Base(path), data, perm)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp) }()
	if err := os.Link(temp, path); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync parent of %s: %w", path, err)
	}
	return nil
}

// ReplaceJSON atomically replaces an owned JSON file after validating its
// current bytes. Symlinks and non-regular targets are rejected.
func ReplaceJSON(path string, value any, perm os.FileMode, validateCurrent func([]byte) error) error {
	current, err := ReadRegularFile(path)
	if err != nil {
		return err
	}
	if validateCurrent != nil {
		if err := validateCurrent(current); err != nil {
			return fmt.Errorf("validate current %s: %w", path, err)
		}
	}
	if err := CleanupAtomicTemps(path); err != nil {
		return err
	}
	data, err := marshalJSON(value)
	if err != nil {
		return err
	}
	temp, err := writeTemp(filepath.Dir(path), filepath.Base(path), data, perm)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp) }()
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync parent of %s: %w", path, err)
	}
	return nil
}

// CleanupAtomicTemps removes only regular temporary files created by this
// package for the exact target basename. Callers must already hold ownership
// locks for the target state.
func CleanupAtomicTemps(path string) error {
	dir := filepath.Dir(path)
	prefix := atomicTempPrefix(filepath.Base(path))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	removed := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("atomic temporary path is not a regular file: %s", filepath.Join(dir, entry.Name()))
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return syncDirectory(dir)
	}
	return nil
}

func LoadJSON(path string, value any) error {
	data, err := ReadRegularFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: trailing JSON value", path)
		}
		return fmt.Errorf("decode %s trailing data: %w", path, err)
	}
	return nil
}

func ReadRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeTemp(dir, targetName string, data []byte, perm os.FileMode) (string, error) {
	file, err := os.CreateTemp(dir, atomicTempPrefix(targetName)+"*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(perm); err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func atomicTempPrefix(targetName string) string {
	if strings.HasPrefix(targetName, ".") {
		return targetName + "-"
	}
	return "." + targetName + "-"
}
