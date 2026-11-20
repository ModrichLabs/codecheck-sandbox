// Package platform checks that the host can run the sandbox at all: CPU
// architecture, KVM, systemd, cgroups v2, memory, disk and a privilege tool.
// It also reports the firewall managers and security module it finds.
package platform

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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
	// LookupGroup names a group the way NSS does, so groups from
	// /usr/lib/group, userdb or LDAP count as well as /etc/group.
	LookupGroup func(gid uint32) (string, error)
}

// DefaultEnv reads the real host.
func DefaultEnv() Env {
	run := system.ExecRunner{}
	return Env{
		FS:          system.OSFS{},
		Runner:      run,
		Arch:        runtime.GOARCH,
		Stat:        statFile,
		FreeBytes:   freeBytes,
		LookPath:    exec.LookPath,
		LookupGroup: getentGroup(run),
	}
}

// getentGroup asks getent, which goes through NSS even in a binary built
// without cgo (where os/user reads only /etc/group).
func getentGroup(r system.Runner) func(uint32) (string, error) {
	return func(gid uint32) (string, error) {
		res, err := r.Run(context.Background(), system.Cmd{Name: "getent", Args: []string{"group", strconv.FormatUint(uint64(gid), 10)}})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "", fmt.Errorf("getent group %d: no such group", gid)
		}
		name, _, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), ":")
		return name, nil
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

// freeBytes reports the free space on path's filesystem as root sees it,
// reserved blocks included, since root writes everything under /var/lib.
func freeBytes(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return s.Bfree * uint64(s.Bsize), nil
}

// ctxOrBackground keeps check signatures tidy where no context arrives.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
