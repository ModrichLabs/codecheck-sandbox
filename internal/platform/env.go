// Package platform checks that the host can run the sandbox at all: CPU
// architecture, KVM, systemd, cgroups v2, memory, disk and a privilege tool.
// It also reports the firewall managers and security module it finds.
package platform

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"syscall"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// FileStat holds what the KVM check needs from a file: its mode and owner.
type FileStat struct {
	Mode     fs.FileMode
	UID, GID uint32
}

// Env supplies everything the checks read, so tests can fake the host.
type Env struct {
	FS        system.FS
	Runner    system.Runner
	Arch      string // runtime.GOARCH of the host
	Stat      func(path string) (FileStat, error)
	FreeBytes func(path string) (uint64, error)
	LookPath  func(name string) (string, error)
}

// DefaultEnv reads the real host.
func DefaultEnv() Env {
	return Env{
		FS:        system.OSFS{},
		Runner:    system.ExecRunner{},
		Arch:      runtime.GOARCH,
		Stat:      statFile,
		FreeBytes: freeBytes,
		LookPath:  exec.LookPath,
	}
}

func statFile(path string) (FileStat, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileStat{}, err
	}
	st := FileStat{Mode: info.Mode()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		st.UID, st.GID = sys.Uid, sys.Gid
	}
	return st, nil
}

// freeBytes reports the space an unprivileged user may still use on path's
// filesystem.
func freeBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return s.Bavail * uint64(s.Bsize), nil
}

// ctxOrBackground keeps check signatures tidy where no context arrives.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
