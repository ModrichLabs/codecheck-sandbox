package guestinit

import (
	"io"
	"os"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Sys holds the kernel calls the guest init makes, so tests can use a fake.
type Sys interface {
	Mount(source, target, fstype string, flags uintptr, data string) error
	Unmount(target string) error
	MkdirAll(path string, mode os.FileMode) error
	WriteFile(path string, data []byte, mode os.FileMode) error
	// Create makes (or truncates) a file the caller writes and closes.
	Create(path string, mode os.FileMode) (io.WriteCloser, error)
	// ReadDevice reads at most n bytes from the start of a block device.
	ReadDevice(path string, n int) ([]byte, error)
	Sync()
}

// Env bundles what every guest job needs.
type Env struct {
	Sys Sys
	Run system.Runner
}

// RealSys makes the real system calls.
type RealSys struct{}

// Mount implements Sys.
func (RealSys) Mount(source, target, fstype string, flags uintptr, data string) error {
	return unix.Mount(source, target, fstype, flags, data)
}

// Unmount implements Sys.
func (RealSys) Unmount(target string) error { return unix.Unmount(target, 0) }

// MkdirAll implements Sys.
func (RealSys) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }

// WriteFile implements Sys.
func (RealSys) WriteFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}

// Create implements Sys.
func (RealSys) Create(path string, mode os.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|unix.O_NOFOLLOW, mode)
}

// ReadDevice implements Sys.
func (RealSys) ReadDevice(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	got, err := io.ReadFull(f, b)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return b[:got], nil
}

// Sync implements Sys.
func (RealSys) Sync() { unix.Sync() }
