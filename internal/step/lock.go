package step

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// LockPath holds the lock that setup, rebuild, uninstall and every review
// share, so two of them never touch the footprint at once.
const LockPath = "/run/codecheck-sandbox/lock"

// ErrBusy reports a lock another process still held when the timeout ran out.
var ErrBusy = errors.New("another codecheck-sandbox command holds the lock")

// lockPoll sets how often Lock retries a held lock.
const lockPoll = 20 * time.Millisecond

// FileLock holds an exclusive flock until Unlock or process exit.
type FileLock struct{ f *os.File }

// Lock takes an exclusive flock on path, creating the file if needed, and
// retries until timeout before giving up with ErrBusy. flock locks belong to
// the open file, so two Lock calls conflict even inside one process.
func Lock(path string, timeout time.Duration) (*FileLock, error) {
	// O_RDONLY suffices for flock, and lets a user open a root-owned 0644
	// lock file that cc-launch created.
	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening the lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &FileLock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, ErrBusy)
		}
		time.Sleep(lockPoll)
	}
}

// Unlock releases the lock.
func (l *FileLock) Unlock() error {
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); err != nil {
		l.f.Close()
		return err
	}
	return l.f.Close()
}
