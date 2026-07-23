package project

import (
	"fmt"
	"os"
	"path/filepath"
)

const lockFile = ".goworktree.lock"

type Lock struct{ path string }

// AcquireLock serializes mutations for one project. Stale locks are never
// removed automatically because a second process could still be active.
func AcquireLock(projectDir string) (*Lock, error) {
	path := filepath.Join(projectDir, lockFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil, fmt.Errorf("project is busy; remove %s only after confirming no command is running", path)
	}
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &Lock{path: path}, nil
}

func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	return os.Remove(l.path)
}
