package step

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// lockFile creates the lock file, as tmpfiles.d does at boot.
func lockFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLockSecondLockerGetsErrBusy(t *testing.T) {
	path := lockFile(t)
	first, err := Lock(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	defer first.Unlock()

	start := time.Now()
	_, err = Lock(context.Background(), path, 100*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second Lock err = %v, want ErrBusy", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond || waited > time.Second {
		t.Errorf("second Lock gave up after %v, want about the 100ms timeout", waited)
	}
}

func TestLockFreesOnUnlock(t *testing.T) {
	path := lockFile(t)
	first, err := Lock(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = first.Unlock()
	}()
	second, err := Lock(context.Background(), path, 2*time.Second)
	if err != nil {
		t.Fatalf("Lock after Unlock: %v", err)
	}
	_ = second.Unlock()
}

func TestLockStopsWhenTheContextEnds(t *testing.T) {
	path := lockFile(t)
	first, err := Lock(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	defer first.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Lock(ctx, path, time.Minute)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's error", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("Lock waited %v after the context ended", waited)
	}
}

func TestLockNeverCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	_, err := Lock(context.Background(), path, 10*time.Millisecond)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Lock created %s; root's tmpfiles.d entry must own it", path)
	}
}
