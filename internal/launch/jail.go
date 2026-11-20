package launch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Disk sizes cc-launch creates. Both start sparse.
const (
	OutDiskSize    = 512 << 20
	TargetDiskSize = 3 << 30
)

// ErrTimeout reports a VM that outlived its time limit and got killed.
var ErrTimeout = errors.New("the VM ran past its time limit")

// vmShape fixes each mode's resources and time limit; nothing the caller
// sends changes them.
type vmShape struct {
	vcpus, memMiB int
	limit         time.Duration
}

var shapes = map[guestinit.Mode]vmShape{
	guestinit.ModeReview:   {2, 2048, 60 * time.Minute},
	guestinit.ModeSelftest: {1, 512, 10 * time.Minute},
	guestinit.ModeBuild:    {2, 1024, 30 * time.Minute},
}

// Limit returns the mode's time limit.
func Limit(m guestinit.Mode) time.Duration { return shapes[m].limit }

const tap = "cc-tap0"

// Mounter makes the jail's bind mounts.
type Mounter interface {
	// BindRO mounts src over dst (an existing file) read-only.
	BindRO(src, dst string) error
	Unmount(path string) error
}

// RealMounter makes real mounts.
type RealMounter struct{}

// BindRO implements Mounter: bind, then remount read-only, since a bind
// ignores MS_RDONLY on the first call (spike S1).
func (RealMounter) BindRO(src, dst string) error {
	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if err := unix.Mount("", dst, "", flags, ""); err != nil {
		_ = unix.Unmount(dst, 0)
		return err
	}
	return nil
}

// Unmount implements Mounter.
func (RealMounter) Unmount(path string) error { return unix.Unmount(path, 0) }

// Jail describes one staged chroot.
type Jail struct {
	Dir     string // JailerBase/firecracker/<id>, which the jailer owns the inside of
	Root    string // Dir/root, the chroot
	Unit    string // the systemd scope the VM runs in
	OutDisk string // the disk the host reads afterwards, inside Root
	binds   []string
}

// Stage builds the chroot for job: read-only binds of the shared kernel and
// root disk (reviews and selftests), copies of the run's own files, a fresh
// writable disk for the jail user, and vm.json.
func Stage(ctx context.Context, e Env, m Mounter, job Job) (j *Jail, err error) {
	dir := filepath.Join(e.Paths.JailerBase, filepath.Base(e.Paths.Firecracker), job.ID.String())
	j = &Jail{Dir: dir, Root: filepath.Join(dir, "root"), Unit: "codecheck-" + job.ID.String() + ".scope"}
	if _, err := os.Lstat(dir); err == nil {
		return nil, fmt.Errorf("%s already exists: a run with this id ran before", dir)
	}
	if err := os.MkdirAll(j.Root, 0o755); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = j.Cleanup(e, m)
		}
	}()

	shape := shapes[job.Config.Mode]
	run := job.ID.Dir(e.Paths)
	vm := guestinit.VMConfig{Mode: job.Config.Mode, Kernel: "vmlinux", ConfigDisk: "config.img",
		VCPUs: shape.vcpus, MemMiB: shape.memMiB}
	copies := []string{"config.img"}
	var fresh string
	var freshSize int64

	if job.Config.Mode == guestinit.ModeBuild {
		copies = append(copies, "vmlinux", "init.cpio", "base.squashfs", "inputs.ext4")
		vm.Initrd, vm.BaseDisk, vm.InputsDisk, vm.TargetDisk = "init.cpio", "base.squashfs", "inputs.ext4", "rootfs.ext4"
		fresh, freshSize = "rootfs.ext4", TargetDiskSize
	} else {
		for _, b := range [][2]string{{e.Paths.Kernel, "vmlinux"}, {e.Paths.RootDisk, "rootfs.ext4"}} {
			dst := filepath.Join(j.Root, b[1])
			if err := os.WriteFile(dst, nil, 0o444); err != nil {
				return j, err
			}
			if err := m.BindRO(b[0], dst); err != nil {
				return j, fmt.Errorf("binding %s: %w", b[0], err)
			}
			j.binds = append(j.binds, dst)
		}
		copies = append(copies, "code.ext4")
		vm.RootDisk, vm.CodeDisk, vm.OutDisk, vm.Tap = "rootfs.ext4", "code.ext4", "out.ext4", tap
		fresh, freshSize = "out.ext4", OutDiskSize
	}

	for _, name := range copies {
		if err := copyFile(filepath.Join(run, name), filepath.Join(j.Root, name), 0o444); err != nil {
			return j, err
		}
	}
	j.OutDisk = filepath.Join(j.Root, fresh)
	if err := makeDisk(ctx, e, j.OutDisk, freshSize); err != nil {
		return j, err
	}

	b, err := guestinit.RenderVM(vm)
	if err != nil {
		return j, err
	}
	return j, os.WriteFile(filepath.Join(j.Root, "vm.json"), b, 0o444)
}

// copyFile copies src to a new file dst with mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// makeDisk creates a sparse ext4 disk owned by the jail user, mode 0600.
func makeDisk(ctx context.Context, e Env, path string, size int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	res, err := e.Run.Run(ctx, system.Cmd{Name: "mkfs.ext4", Args: []string{"-q", "-F", "-E", "root_owner=0:0", path}, CleanEnv: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("mkfs.ext4 %s: %s", path, strings.TrimSpace(string(res.Stderr)))
	}
	return os.Chown(path, e.JailUID, e.JailGID)
}

// Argv returns the command that boots the VM: a transient scope with the
// mode's time limit, around the jailer with spike S1's flags. The jailer's
// cgroup goes inside the scope, so the time limit and the wait cover
// Firecracker after --new-pid-ns detaches it.
func (j *Jail) Argv(e Env, job Job) []string {
	shape := shapes[job.Config.Mode]
	limit := int64(OutDiskSize)
	if job.Config.Mode == guestinit.ModeBuild {
		limit = TargetDiskSize
	}
	return []string{
		"systemd-run", "--scope", "--quiet", "--collect",
		"--unit=" + strings.TrimSuffix(j.Unit, ".scope"), "--slice=system.slice",
		"-p", "RuntimeMaxSec=" + strconv.Itoa(int(shape.limit.Seconds())),
		"--",
		e.Paths.Jailer,
		"--id", job.ID.String(),
		"--exec-file", e.Paths.Firecracker,
		"--uid", strconv.Itoa(e.JailUID),
		"--gid", strconv.Itoa(e.JailGID),
		"--chroot-base-dir", e.Paths.JailerBase,
		"--cgroup-version", "2",
		"--parent-cgroup", "system.slice/" + j.Unit,
		"--new-pid-ns",
		"--resource-limit", "fsize=" + strconv.FormatInt(limit, 10),
		"--resource-limit", "no-file=1024",
		"--",
		"--no-api", "--config-file", "vm.json",
	}
}

// killGrace bounds the wait for a killed scope to empty.
var killGrace = 30 * time.Second

// ErrStuck reports a scope still populated after the kill; the caller must
// leave the jail in place.
var ErrStuck = errors.New("the VM's scope still holds processes after a kill")

// Wait polls the scope's cgroup.events until nothing runs in it (or the
// scope has gone). Past limit plus a tenth it kills the scope and returns
// ErrTimeout once the scope empties, or ErrStuck after killGrace. The caller
// cleans up only after a nil or ErrTimeout result.
func (j *Jail) Wait(ctx context.Context, e Env, limit, poll time.Duration) error {
	events := filepath.Join(e.Paths.CgroupRoot, "system.slice", j.Unit, "cgroup.events")
	deadline := time.Now().Add(limit + limit/10)
	killed := false
	var killedAt time.Time
	for {
		b, err := e.Proc.ReadFile(events)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if strings.Contains("\n"+string(b), "\npopulated 0") {
			break
		}
		if !killed && time.Now().After(deadline) {
			if _, err := e.Run.Run(ctx, system.Cmd{Name: "systemctl", Args: []string{"kill", "--signal=SIGKILL", j.Unit}, CleanEnv: true}); err != nil {
				return err
			}
			killed, killedAt = true, time.Now()
		}
		if killed && time.Since(killedAt) > killGrace {
			return errors.Join(ErrTimeout, ErrStuck)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	if killed {
		return ErrTimeout
	}
	return nil
}

// Cleanup unmounts the binds and removes the jail folder, but only once
// /proc/self/mountinfo shows no mount left inside it: removing files under a
// live bind could reach the shared images.
func (j *Jail) Cleanup(e Env, m Mounter) error {
	var errs []error
	for i := len(j.binds) - 1; i >= 0; i-- {
		if err := m.Unmount(j.binds[i]); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, fmt.Errorf("unmount %s: %w", j.binds[i], err))
		}
	}
	info, err := e.Proc.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, line := range strings.Split(string(info), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && (f[4] == j.Dir || strings.HasPrefix(f[4], j.Dir+"/")) {
			return errors.Join(append(errs, fmt.Errorf("%s still holds a mount at %s; left in place", j.Dir, f[4]))...)
		}
	}
	if err := os.RemoveAll(j.Dir); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
