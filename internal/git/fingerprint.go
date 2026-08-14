package git

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

// WorktreeFingerprintContext returns a stable SHA-256 fingerprint of the
// worktree state that a forced removal or reset can destroy. It includes the
// logical Git index and every on-disk entry below repo except its top-level
// .git administrative entry. Symlinks are represented by their targets and
// are never traversed.
func WorktreeFingerprintContext(ctx context.Context, repo string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	root, err := filepath.Abs(repo)
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect worktree %s: %w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("worktree is not a directory: %s", root)
	}

	h := sha256.New()
	writeFingerprintRecord(h, "version", []byte("goworktree-worktree-fingerprint-v1"))

	index, err := runContext(ctx, root, "--no-optional-locks", "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return "", fmt.Errorf("read index state for %s: %w", root, err)
	}
	writeFingerprintRecord(h, "index", []byte(index))

	if err := fingerprintDirectory(ctx, h, root, "", true); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func fingerprintDirectory(ctx context.Context, h hash.Hash, root, relative string, topLevel bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	directory := root
	if relative != "" {
		directory = filepath.Join(root, relative)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read worktree directory %s: %w", directory, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if topLevel && entry.Name() == ".git" {
			continue
		}

		childRelative := entry.Name()
		if relative != "" {
			childRelative = filepath.Join(relative, entry.Name())
		}
		path := filepath.Join(root, childRelative)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect worktree entry %s: %w", path, err)
		}
		canonicalPath := filepath.ToSlash(childRelative)

		switch {
		case info.Mode().IsRegular():
			contentDigest, size, err := fingerprintFileContent(ctx, path)
			if err != nil {
				return err
			}
			writeFingerprintRecord(h, "file", []byte(canonicalPath), modeBytes(info.Mode()), int64Bytes(size), contentDigest)
		case info.IsDir():
			writeFingerprintRecord(h, "directory", []byte(canonicalPath), modeBytes(info.Mode()))
			if err := fingerprintDirectory(ctx, h, root, childRelative, false); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("read worktree symlink %s: %w", path, err)
			}
			writeFingerprintRecord(h, "symlink", []byte(canonicalPath), []byte(target))
		default:
			return fmt.Errorf("unsafe worktree entry type at %s: %s", path, info.Mode().Type())
		}
	}
	return nil
}

func fingerprintFileContent(ctx context.Context, path string) ([]byte, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open worktree file %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("inspect opened worktree file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("worktree file changed type while fingerprinting: %s", path)
	}
	digest := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = digest.Write(buffer[:read])
			size += int64(read)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, 0, fmt.Errorf("read worktree file %s: %w", path, readErr)
		}
	}
	return digest.Sum(nil), size, nil
}

func modeBytes(mode os.FileMode) []byte {
	var encoded [4]byte
	preserved := mode.Perm() | mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)
	binary.BigEndian.PutUint32(encoded[:], uint32(preserved))
	return encoded[:]
}

func int64Bytes(value int64) []byte {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	return encoded[:]
}

// writeFingerprintRecord uses a type tag followed by length-framed fields, so
// record boundaries are unambiguous even when paths or contents contain NULs.
func writeFingerprintRecord(h hash.Hash, kind string, fields ...[]byte) {
	writeFingerprintField(h, []byte(kind))
	for _, field := range fields {
		writeFingerprintField(h, field)
	}
}

func writeFingerprintField(w io.Writer, value []byte) {
	var length [10]byte
	n := binary.PutUvarint(length[:], uint64(len(value)))
	_, _ = w.Write(length[:n])
	_, _ = w.Write(value)
}
