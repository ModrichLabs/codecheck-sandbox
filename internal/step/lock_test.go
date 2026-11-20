package step

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLockSecondLockerGetsErrBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := Lock(path, time.Second)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	defer first.Unlock()

	start := time.Now()
	_, err = Lock(path, 100*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second Lock err = %v, want ErrBusy", err)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond || waited > time.Second {
		t.Errorf("second Lock gave up after %v, want about the 100ms timeout", waited)
	}
}

func TestLockFreesOnUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := Lock(path, time.Second)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = first.Unlock()
	}()
	second, err := Lock(path, 2*time.Second)
	if err != nil {
		t.Fatalf("Lock after Unlock: %v", err)
	}
	_ = second.Unlock()
}

func TestLockMissingFolder(t *testing.T) {
	_, err := Lock(filepath.Join(t.TempDir(), "absent", "lock"), 10*time.Millisecond)
	if err == nil || errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want an open error, not ErrBusy", err)
	}
}
