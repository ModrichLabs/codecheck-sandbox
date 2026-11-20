package guestinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// FileOps holds the file calls that need root (ownership, device nodes) or
// whose order matters (chmod after chown), so tests can record them.
type FileOps interface {
	Lchown(path string, uid, gid int) error
	Chmod(path string, mode os.FileMode) error
	Mknod(path string, mode uint32, dev uint64) error
}

// RealOps makes the real calls.
type RealOps struct{}

// Lchown implements FileOps.
func (RealOps) Lchown(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }

// Chmod implements FileOps.
func (RealOps) Chmod(path string, mode os.FileMode) error { return os.Chmod(path, mode) }

// Mknod implements FileOps.
func (RealOps) Mknod(path string, mode uint32, dev uint64) error {
	return unix.Mknod(path, mode, int(dev))
}

// BuildPaths names where the build mounts its three disks.
type BuildPaths struct {
	Base   string // the Ubuntu squashfs, read-only
	Inputs string // the host's files for the image, read-only
	Target string // the blank ext4 disk that becomes the guest's root
}

// DefaultBuildPaths lives under /mnt in the builder's initramfs.
var DefaultBuildPaths = BuildPaths{Base: "/mnt/base", Inputs: "/mnt/inputs", Target: "/mnt/target"}

// The build VM's drives: the initramfs holds the init, so vda carries the
// base and vdb, as always, the config.
const (
	buildBaseDisk   = "/dev/vda"
	buildInputsDisk = "/dev/vdc"
	buildTargetDisk = "/dev/vdd"
)

// Tools from the target image, run chrooted into it.
const (
	useraddPath  = "/usr/sbin/useradd"
	updateCAPath = "/usr/sbin/update-ca-certificates"
	chrootPath   = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// CACertPath holds the sandbox CA inside the image; cc-claude points
// NODE_EXTRA_CA_CERTS at it.
const CACertPath = "/usr/local/share/ca-certificates/codecheck-sandbox.crt"

// inputFiles maps each file the inputs disk must carry to its place in the
// image and its mode. Owners always become root.
var inputFiles = []struct {
	name, dest string
	mode       os.FileMode
}{
	{"claude", "/usr/local/bin/claude", 0o755},
	{"cc-claude", ccClaudePath, 0o755},
	{"cc-selftest", selftestPath, 0o755},
	{"cc-guestinit", "/usr/local/sbin/cc-guestinit", 0o755},
	{"ca.crt", CACertPath, 0o644},
}

// pluginDest holds the CodeCheck plugin, read-only to the agent.
const pluginDest = "/opt/codecheck/plugin"

// Build assembles the guest root disk, as root inside the builder VM: it
// copies the Ubuntu tree onto the target, adds the inputs, creates the agent
// account, trusts the sandbox CA and marks the disk complete. The host hashes
// the finished disk.
func Build(ctx context.Context, e Env, ops FileOps, c Config, p BuildPaths) (err error) {
	if c.Mode != ModeBuild {
		return fmt.Errorf("Build: mode %q", c.Mode)
	}
	for _, m := range []struct {
		source, target, fstype string
		flags                  uintptr
		data                   string
	}{
		{buildBaseDisk, p.Base, "squashfs", unix.MS_RDONLY, ""},
		{buildInputsDisk, p.Inputs, "ext4", unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, "noload"},
		{buildTargetDisk, p.Target, "ext4", 0, ""},
	} {
		if err := e.Sys.MkdirAll(m.target, 0o755); err != nil {
			return err
		}
		if err := e.Sys.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	defer func() {
		e.Sys.Sync()
		for _, t := range []string{p.Target, p.Inputs, p.Base} {
			if uerr := e.Sys.Unmount(t); uerr != nil && err == nil {
				err = fmt.Errorf("unmount %s: %w", t, uerr)
			}
		}
	}()

	if err := CopyTree(p.Base, p.Target, ops); err != nil {
		return fmt.Errorf("copying the base tree: %w", err)
	}
	if err := installInputs(p, ops); err != nil {
		return err
	}
	if err := inChroot(ctx, e, p.Target); err != nil {
		return err
	}
	marker := filepath.Join(p.Target, "etc/codecheck-sandbox")
	if err := e.Sys.MkdirAll(marker, 0o755); err != nil {
		return err
	}
	return e.Sys.WriteFile(filepath.Join(marker, "build-complete"), []byte(c.RunID+"\n"), 0o644)
}

// installInputs copies the host's files into the image with fixed owners and
// modes, and makes the mount points the review VM needs.
func installInputs(p BuildPaths, ops FileOps) error {
	for _, f := range inputFiles {
		if err := installFile(filepath.Join(p.Inputs, f.name), filepath.Join(p.Target, f.dest), f.mode, ops); err != nil {
			return fmt.Errorf("installing %s: %w", f.name, err)
		}
	}
	if err := installPlugin(filepath.Join(p.Inputs, "plugin"), filepath.Join(p.Target, pluginDest), ops); err != nil {
		return fmt.Errorf("installing the plugin: %w", err)
	}
	for _, d := range []string{"/work", "/out", AgentHome} {
		if err := mkdirRoot(filepath.Join(p.Target, d), ops); err != nil {
			return err
		}
	}
	return nil
}

// installPlugin copies a tree of plain files and folders, refusing links and
// special files; files keep only whether they run.
func installPlugin(src, dst string, ops FileOps) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return mkdirRoot(out, ops)
		case d.Type().IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if fi.Mode()&0o111 != 0 {
				mode = 0o755
			}
			return installFile(path, out, mode, ops)
		default:
			return fmt.Errorf("%s: not a plain file or folder", rel)
		}
	})
}

// mkdirRoot makes dir and its missing parents as root-owned 0755 folders.
func mkdirRoot(dir string, ops FileOps) error {
	if fi, err := os.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s: not a folder", dir)
		}
		return nil
	}
	if err := mkdirRoot(filepath.Dir(dir), ops); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := ops.Lchown(dir, 0, 0); err != nil {
		return err
	}
	return ops.Chmod(dir, 0o755)
}

// installFile copies one regular file to a new root-owned file.
func installFile(src, dst string, mode os.FileMode, ops FileOps) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s: not a plain file", src)
	}
	if err := mkdirRoot(filepath.Dir(dst), ops); err != nil {
		return err
	}
	if err := copyContents(src, dst); err != nil {
		return err
	}
	if err := ops.Lchown(dst, 0, 0); err != nil {
		return err
	}
	return ops.Chmod(dst, mode)
}

// copyContents writes src's bytes to dst, which must not exist yet: O_EXCL
// with O_NOFOLLOW means nothing planted in the target redirects the write.
func copyContents(src, dst string) error {
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
	return out.Close()
}

// inChroot runs the target's own useradd and update-ca-certificates inside
// it, with /dev and /proc bound in for the duration.
func inChroot(ctx context.Context, e Env, target string) (err error) {
	var bound []string
	defer func() {
		for i := len(bound) - 1; i >= 0; i-- {
			if uerr := e.Sys.Unmount(bound[i]); uerr != nil && err == nil {
				err = fmt.Errorf("unmount %s: %w", bound[i], uerr)
			}
		}
	}()
	for _, d := range []string{"/dev", "/proc"} {
		at := filepath.Join(target, d)
		if err := e.Sys.MkdirAll(at, 0o755); err != nil {
			return err
		}
		if err := e.Sys.Mount(d, at, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s: %w", d, err)
		}
		bound = append(bound, at)
	}
	uid := strconv.Itoa(AgentUID)
	for _, c := range []system.Cmd{
		{Name: useraddPath, Args: []string{"--uid", uid, "--user-group", "--home-dir", AgentHome,
			"--no-create-home", "--shell", "/usr/sbin/nologin", "agent"}},
		{Name: updateCAPath},
	} {
		c.Chroot, c.Dir, c.CleanEnv = target, "/", true
		c.Env = []string{"PATH=" + chrootPath}
		res, err := e.Run.Run(ctx, c)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("%s: exit %d: %s", c.Name, res.Code, strings.TrimSpace(string(res.Stderr)))
		}
	}
	return nil
}

// CopyTree copies src onto dst entry by entry, keeping owners, modes
// (setuid, setgid and sticky included), symlinks, hard links, device nodes
// and modification times. It drops extended attributes, file capabilities
// among them: the agent never needs more than plain permissions give.
func CopyTree(src, dst string, ops FileOps) error {
	type inode struct{ dev, ino uint64 }
	seen := map[inode]string{}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		fi, err := os.Lstat(path)
		if err != nil {
			return err
		}
		var stt unix.Stat_t
		if err := unix.Lstat(path, &stt); err != nil {
			return err
		}

		if rel == "." {
			return applyMeta(out, fi, &stt, ops)
		}
		mode := fi.Mode()
		switch {
		case mode.IsDir():
			if err := os.Mkdir(out, 0o700); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, out); err != nil {
				return err
			}
			if err := ops.Lchown(out, int(stt.Uid), int(stt.Gid)); err != nil {
				return err
			}
			return setTimes(out, &stt)
		case mode.IsRegular():
			key := inode{uint64(stt.Dev), stt.Ino}
			if first, ok := seen[key]; ok && stt.Nlink > 1 {
				return os.Link(first, out)
			}
			if err := copyContents(path, out); err != nil {
				return err
			}
			if stt.Nlink > 1 {
				seen[key] = out
			}
		default: // devices, fifos and sockets
			if err := ops.Mknod(out, stt.Mode&(unix.S_IFMT|0o7777), uint64(stt.Rdev)); err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
		}
		return applyMeta(out, fi, &stt, ops)
	})
}

// applyMeta sets owner, then mode (chown clears setuid), then times.
func applyMeta(out string, fi os.FileInfo, st *unix.Stat_t, ops FileOps) error {
	if err := ops.Lchown(out, int(st.Uid), int(st.Gid)); err != nil {
		return err
	}
	if err := ops.Chmod(out, fi.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
		return err
	}
	return setTimes(out, st)
}

// setTimes copies the access and modification times without following a
// symlink.
func setTimes(out string, st *unix.Stat_t) error {
	ts := []unix.Timespec{st.Atim, st.Mtim}
	err := unix.UtimesNanoAt(unix.AT_FDCWD, out, ts, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("%s vanished during the copy", out)
	}
	return err
}
