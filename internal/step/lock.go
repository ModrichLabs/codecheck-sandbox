package step

import (
	"context"
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

// Lock takes an exclusive flock on path and retries until timeout before
// giving up with ErrBusy, or until ctx ends. flock locks belong to the open
// file, so two Lock calls conflict even inside one process.
//
// Lock never creates the file: a tmpfiles.d entry creates it at boot, owned
// by root with mode 0644. A user-created file in a shared folder would stop
// root's cc-launch from opening it under fs.protected_regular. Read-only
// access suffices for flock.
func Lock(ctx context.Context, path string, timeout time.Duration) (*FileLock, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the lock: %w", err)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(lockPoll)
	defer poll.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &FileLock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("waiting for %s: %w", path, ctx.Err())
		case <-deadline.C:
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, ErrBusy)
		case <-poll.C:
		}
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
