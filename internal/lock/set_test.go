package lock

import (
	"context"
	"errors"
	"testing"
)

func TestSetRejectsHeldLockAndReleasesPartialAcquisition(t *testing.T) {
	t.Parallel()

	set := Set{Root: t.TempDir()}
	first, err := set.Acquire(context.Background(), "repositories", []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()

	if _, err := set.Acquire(context.Background(), "repositories", []string{"a", "b"}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire error = %v, want ErrLocked", err)
	}

	// The failed request acquired a first, then had to release it when b failed.
	a, err := set.Acquire(context.Background(), "repositories", []string{"a"})
	if err != nil {
		t.Fatalf("partial lock was not released: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("Release is not idempotent: %v", err)
	}
}
