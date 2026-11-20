package guestinit

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeOps records ownership changes (which need root) and performs the rest
// for real, logging the order of chown and chmod per path.
type fakeOps struct {
	owners map[string][2]int
	log    []string // "chown path" / "chmod path"
}

func newFakeOps() *fakeOps { return &fakeOps{owners: map[string][2]int{}} }

func (o *fakeOps) Lchown(path string, uid, gid int) error {
	o.owners[path] = [2]int{uid, gid}
	o.log = append(o.log, "chown "+path)
	return nil
}

func (o *fakeOps) Chmod(path string, mode os.FileMode) error {
	o.log = append(o.log, "chmod "+path)
	return os.Chmod(path, mode)
}

func (o *fakeOps) Mknod(path string, mode uint32, dev uint64) error {
	return unix.Mknod(path, mode, int(dev))
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCopyTreeKeepsOwnersModesAndLinks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "usr/bin/sudo"), "sudo", 0o755)
	if err := os.Chmod(filepath.Join(src, "usr/bin/sudo"), 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "etc/shadow"), "secret", 0o640)
	if err := os.Symlink("../usr/bin/sudo", filepath.Join(src, "etc/sudo-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(src, "etc/shadow"), filepath.Join(src, "etc/shadow-hard")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(src, "etc/fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(src, "tmp"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "tmp"), 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2024, 4, 25, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(src, "etc/shadow"), old, old); err != nil {
		t.Fatal(err)
	}

	ops := newFakeOps()
	if err := CopyTree(src, dst, ops); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}

	sudo := filepath.Join(dst, "usr/bin/sudo")
	if fi, err := os.Lstat(sudo); err != nil || fi.Mode() != 0o755|os.ModeSetuid {
		t.Errorf("sudo mode = %v, %v; want setuid 0755", fi.Mode(), err)
	}
	// chown clears setuid, so the mode must come after the owner.
	if i, j := slices.Index(ops.log, "chown "+sudo), slices.Index(ops.log, "chmod "+sudo); i < 0 || j < i {
		t.Errorf("sudo: chown at %d, chmod at %d; want chown first", i, j)
	}
	uid, gid := os.Getuid(), os.Getgid()
	for _, p := range []string{"usr/bin/sudo", "etc/shadow", "etc/sudo-link", "etc/fifo", "tmp", "usr"} {
		if got, ok := ops.owners[filepath.Join(dst, p)]; !ok || got != [2]int{uid, gid} {
			t.Errorf("%s: owner %v, want %d:%d from the source", p, got, uid, gid)
		}
	}
	if fi, _ := os.Lstat(filepath.Join(dst, "etc/shadow")); fi.Mode() != 0o640 || !fi.ModTime().Equal(old) {
		t.Errorf("shadow: mode %v, mtime %v; want 0640 and %v", fi.Mode(), fi.ModTime(), old)
	}
	if target, err := os.Readlink(filepath.Join(dst, "etc/sudo-link")); err != nil || target != "../usr/bin/sudo" {
		t.Errorf("symlink = %q, %v", target, err)
	}
	a, _ := os.Stat(filepath.Join(dst, "etc/shadow"))
	b, _ := os.Stat(filepath.Join(dst, "etc/shadow-hard"))
	if !os.SameFile(a, b) {
		t.Error("the hard link became two files")
	}
	if fi, _ := os.Lstat(filepath.Join(dst, "etc/fifo")); fi.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("fifo mode = %v", fi.Mode())
	}
	if fi, _ := os.Lstat(filepath.Join(dst, "tmp")); fi.Mode() != os.ModeDir|os.ModeSticky|0o777 {
		t.Errorf("tmp mode = %v, want sticky 0777", fi.Mode())
	}
}

func TestCopyTreeRefusesAnExistingTarget(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "passwd"), "new", 0o644)
	// A symlink planted in the target must not redirect the write.
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Symlink(outside, filepath.Join(dst, "passwd")); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(src, dst, newFakeOps()); err == nil {
		t.Error("CopyTree wrote over an existing entry")
	}
	if _, err := os.Lstat(outside); err == nil {
		t.Error("CopyTree followed a symlink out of the target")
	}
}

// buildFixture lays out a base tree, an inputs disk and a blank target.
func buildFixture(t *testing.T) BuildPaths {
	t.Helper()
	p := BuildPaths{Base: t.TempDir(), Inputs: t.TempDir(), Target: t.TempDir()}
	writeFile(t, filepath.Join(p.Base, "etc/os-release"), "ID=ubuntu\n", 0o644)
	if err := os.MkdirAll(filepath.Join(p.Base, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{
		"claude": 0o700, "cc-claude": 0o700, "cc-selftest": 0o700, "cc-guestinit": 0o700, "ca.crt": 0o600,
	} {
		writeFile(t, filepath.Join(p.Inputs, name), name, mode)
	}
	writeFile(t, filepath.Join(p.Inputs, "plugin/skills/codecheck/SKILL.md"), "skill", 0o600)
	writeFile(t, filepath.Join(p.Inputs, "plugin/bin/helper"), "#!/bin/sh\n", 0o700)
	return p
}

func scriptChroot(f *system.FakeRunner) {
	f.On(useraddPath).Return(system.Result{})
	f.On(updateCAPath).Return(system.Result{})
}

func TestBuild(t *testing.T) {
	p := buildFixture(t)
	s, ops, f := newFakeSys(), newFakeOps(), &system.FakeRunner{}
	scriptChroot(f)

	if err := Build(context.Background(), Env{Sys: s, Run: f}, ops, Config{Mode: ModeBuild, RunID: "b1"}, p); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Disks: the base read-only, the inputs read-only and inert, the target writable.
	for _, want := range []struct {
		source, target, fstype string
		flags                  uintptr
	}{
		{"/dev/vda", p.Base, "squashfs", unix.MS_RDONLY},
		{"/dev/vdc", p.Inputs, "ext4", unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"/dev/vdd", p.Target, "ext4", 0},
	} {
		m, ok := s.mountAt(want.target)
		if !ok || m.Source != want.source || m.FSType != want.fstype || m.Flags&want.flags != want.flags {
			t.Errorf("mount %s: got %+v, want %s %s", want.target, m, want.source, want.fstype)
		}
	}
	if m, _ := s.mountAt(p.Target); m.Flags&unix.MS_RDONLY != 0 {
		t.Error("the target disk mounted read-only")
	}

	// The base tree arrived.
	if _, err := os.Stat(filepath.Join(p.Target, "etc/os-release")); err != nil {
		t.Errorf("base tree not copied: %v", err)
	}
	// The inputs land root-owned with fixed modes, whatever the inputs disk says.
	for path, mode := range map[string]os.FileMode{
		"usr/local/bin/claude":                                  0o755,
		"usr/local/bin/cc-claude":                               0o755,
		"usr/local/bin/cc-selftest":                             0o755,
		"usr/local/sbin/cc-guestinit":                           0o755,
		"usr/local/share/ca-certificates/codecheck-sandbox.crt": 0o644,
		"opt/codecheck/plugin/skills/codecheck/SKILL.md":        0o644,
		"opt/codecheck/plugin/bin/helper":                       0o755,
		"opt/codecheck/plugin/skills":                           os.ModeDir | 0o755,
	} {
		full := filepath.Join(p.Target, path)
		fi, err := os.Lstat(full)
		if err != nil || fi.Mode() != mode {
			t.Errorf("%s: mode %v, %v; want %v", path, fi.Mode(), err, mode)
		}
		if ops.owners[full] != [2]int{0, 0} {
			t.Errorf("%s: owner %v, want root", path, ops.owners[full])
		}
	}
	for _, d := range []string{"work", "out", "home/agent"} {
		if fi, err := os.Stat(filepath.Join(p.Target, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s: missing mount point", d)
		}
	}

	// The account and the CA go in through the target's own tools, chrooted.
	calls := f.Calls()
	if len(calls) != 2 {
		t.Fatalf("ran %d commands, want 2", len(calls))
	}
	wantUseradd := []string{"--uid", "10001", "--user-group", "--home-dir", AgentHome,
		"--no-create-home", "--shell", "/usr/sbin/nologin", "agent"}
	if c := calls[0]; c.Name != useraddPath || !slices.Equal(c.Args, wantUseradd) || c.Chroot != p.Target {
		t.Errorf("useradd call = %+v", c)
	}
	if c := calls[1]; c.Name != updateCAPath || c.Chroot != p.Target {
		t.Errorf("update-ca-certificates call = %+v", c)
	}
	// /dev and /proc go into the chroot for those commands, then come out.
	for _, d := range []string{"/dev", "/proc"} {
		m, ok := s.mountAt(filepath.Join(p.Target, d))
		if !ok || m.Source != d || m.Flags&unix.MS_BIND == 0 {
			t.Errorf("%s not bind-mounted into the target: %+v", d, m)
		}
		if !slices.Contains(s.unmounts, filepath.Join(p.Target, d)) {
			t.Errorf("%s left mounted in the target", d)
		}
	}

	if got := s.files[filepath.Join(p.Target, "etc/codecheck-sandbox/build-complete")]; got == nil || got.String() != "b1\n" {
		t.Errorf("build-complete marker = %v", got)
	}
	if s.synced == 0 || !slices.Contains(s.unmounts, p.Target) {
		t.Error("Build didn't sync and unmount the target")
	}
}

func TestBuildRefusesAMissingInput(t *testing.T) {
	p := buildFixture(t)
	if err := os.Remove(filepath.Join(p.Inputs, "ca.crt")); err != nil {
		t.Fatal(err)
	}
	err := Build(context.Background(), Env{Sys: newFakeSys(), Run: &system.FakeRunner{}}, newFakeOps(), Config{Mode: ModeBuild, RunID: "b1"}, p)
	if err == nil {
		t.Fatal("Build carried on without the CA")
	}
}

func TestBuildRefusesALinkInTheInputs(t *testing.T) {
	p := buildFixture(t)
	if err := os.Symlink("/etc/shadow", filepath.Join(p.Inputs, "plugin/skills/leak")); err != nil {
		t.Fatal(err)
	}
	err := Build(context.Background(), Env{Sys: newFakeSys(), Run: &system.FakeRunner{}}, newFakeOps(), Config{Mode: ModeBuild, RunID: "b1"}, p)
	if err == nil {
		t.Fatal("Build installed a symlink from the inputs disk")
	}
}

func TestBuildStopsWhenUseraddFails(t *testing.T) {
	p := buildFixture(t)
	s, f := newFakeSys(), &system.FakeRunner{}
	f.On(useraddPath).Return(system.Result{Code: 9, Stderr: []byte("useradd: UID 10001 is not unique")})
	err := Build(context.Background(), Env{Sys: s, Run: f}, newFakeOps(), Config{Mode: ModeBuild, RunID: "b1"}, p)
	if err == nil {
		t.Fatal("Build carried on after useradd failed")
	}
	if _, ok := s.files[filepath.Join(p.Target, "etc/codecheck-sandbox/build-complete")]; ok {
		t.Error("a failed build wrote the completion marker")
	}
	// The binds still come out, so the target unmounts cleanly.
	if !slices.Contains(s.unmounts, filepath.Join(p.Target, "/dev")) {
		t.Error("a failed build left /dev bound into the target")
	}
}
