package lock

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ErrLocked means another operation owns at least one requested lock.
var ErrLocked = errors.New("locked")

type record struct {
	Key       string    `json:"key"`
	Token     string    `json:"token"`
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
}

// Set stores cooperative cross-process locks outside user Work directories.
// Existing locks are never removed as "stale" automatically.
type Set struct {
	Root string
	Now  func() time.Time
	Rand func([]byte) (int, error)
	PID  int
}

type held struct {
	path  string
	token string
}

type Lease struct {
	held     []held
	released bool
}

// Acquire locks unique keys in stable order. Partial acquisition is rolled
// back if any key is already locked or cannot be persisted.
func (s Set) Acquire(ctx context.Context, namespace string, keys []string) (*Lease, error) {
	if s.Root == "" {
		return nil, fmt.Errorf("lock root is empty")
	}
	if namespace == "" {
		return nil, fmt.Errorf("lock namespace is empty")
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no lock keys supplied")
	}

	ordered := append([]string(nil), keys...)
	sort.Strings(ordered)
	for i := 1; i < len(ordered); i++ {
		if ordered[i] == ordered[i-1] {
			return nil, fmt.Errorf("duplicate lock key %q", ordered[i])
		}
	}

	dir := filepath.Join(s.Root, "locks", namespace)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}

	lease := &Lease{}
	rollback := func(primary error) error {
		return errors.Join(primary, lease.Release())
	}
	for _, key := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, rollback(err)
		}
		token, err := s.token()
		if err != nil {
			return nil, rollback(fmt.Errorf("generate lock token: %w", err))
		}
		rec := record{Key: key, Token: token, PID: s.pid(), CreatedAt: s.now().UTC()}
		data, err := json.Marshal(rec)
		if err != nil {
			return nil, rollback(err)
		}
		path := filepath.Join(dir, hashKey(key)+".lock")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return nil, rollback(fmt.Errorf("%w: %s", ErrLocked, key))
		}
		if err != nil {
			return nil, rollback(fmt.Errorf("acquire lock %q: %w", key, err))
		}
		writeErr := func() error {
			if _, err := file.Write(data); err != nil {
				return err
			}
			return file.Sync()
		}()
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			removeErr := os.Remove(path)
			return nil, rollback(fmt.Errorf("persist lock %q: %w", key, errors.Join(writeErr, closeErr, removeErr)))
		}
		lease.held = append(lease.held, held{path: path, token: token})
	}
	return lease, nil
}

// Release removes only lock files that still contain this lease's token.
func (l *Lease) Release() error {
	if l == nil || l.released {
		return nil
	}
	var errs []error
	for i := len(l.held) - 1; i >= 0; i-- {
		h := l.held[i]
		data, err := os.ReadFile(h.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read lock %s: %w", h.path, err))
			continue
		}
		var rec record
		if err := json.Unmarshal(data, &rec); err != nil {
			errs = append(errs, fmt.Errorf("decode lock %s: %w", h.path, err))
			continue
		}
		if rec.Token != h.token {
			errs = append(errs, fmt.Errorf("lock ownership changed: %s", h.path))
			continue
		}
		if err := os.Remove(h.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("release lock %s: %w", h.path, err))
		}
	}
	err := errors.Join(errs...)
	if err == nil {
		l.released = true
	}
	return err
}

func (s Set) token() (string, error) {
	b := make([]byte, 16)
	read := s.Rand
	if read == nil {
		read = rand.Read
	}
	n, err := read(b)
	if err != nil {
		return "", err
	}
	if n != len(b) {
		return "", fmt.Errorf("random source returned %d bytes; want %d", n, len(b))
	}
	return hex.EncodeToString(b), nil
}

func (s Set) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Set) pid() int {
	if s.PID != 0 {
		return s.PID
	}
	return os.Getpid()
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
