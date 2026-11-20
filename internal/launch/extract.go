package launch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// The account debugfs runs as: it parses a disk the guest wrote, so it gets
// no privilege at all.
const nobody = "65534"

var (
	// ErrTooLarge reports a file over its cap; Extract drops it.
	ErrTooLarge = errors.New("a file on the output disk exceeds its cap")
	// ErrBuildIncomplete reports a build disk without this run's marker.
	ErrBuildIncomplete = errors.New("the build never marked its disk complete")
)

// resultFiles lists what Extract copies off a job's output disk, with caps.
var resultFiles = []struct {
	name string
	cap  int64
}{
	{"stream.jsonl", 200 << 20},
	{"stderr.log", 5 << 20},
	{"exit-code", 16},
}

const buildMarker = "/etc/codecheck-sandbox/build-complete"

// diskName names the writable disk a mode leaves in the run folder.
func diskName(m guestinit.Mode) string {
	if m == guestinit.ModeBuild {
		return "rootfs.ext4"
	}
	return "out.ext4"
}

// CollectOutDisk moves the VM's writable disk from the jail into the run
// folder, read-only and root-owned, so the jail can go and an unprivileged
// debugfs can read the disk.
func CollectOutDisk(e Env, j *Jail, job Job) error {
	dst := filepath.Join(job.ID.Dir(e.Paths), diskName(job.Config.Mode))
	if err := os.Chown(j.OutDisk, e.Owner, e.Owner); err != nil {
		return err
	}
	if err := os.Chmod(j.OutDisk, 0o444); err != nil {
		return err
	}
	err := os.Rename(j.OutDisk, dst)
	if errors.Is(err, unix.EXDEV) {
		if err := copyFile(j.OutDisk, dst, 0o444); err != nil {
			return err
		}
		return os.Remove(j.OutDisk)
	}
	return err
}

// Extract reads the run's writable disk with debugfs as nobody. For a job it
// copies the fixed file list, each within its cap, into a result folder the
// calling user owns, and returns the names it copied; a missing file just
// stays missing. For a build it checks the completion marker instead.
func Extract(ctx context.Context, e Env, job Job) ([]string, error) {
	disk := filepath.Join(job.ID.Dir(e.Paths), diskName(job.Config.Mode))
	if job.Config.Mode == guestinit.ModeBuild {
		var buf bytes.Buffer
		found, err := debugfsCat(ctx, e, disk, buildMarker, &capped{w: &buf, left: 128})
		if err != nil || !found || strings.TrimSpace(buf.String()) != job.ID.String() {
			return nil, errors.Join(ErrBuildIncomplete, err)
		}
		return nil, nil
	}

	result := filepath.Join(job.ID.Dir(e.Paths), "result")
	if err := os.Mkdir(result, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chown(result, e.ResultUID, e.ResultGID); err != nil {
		return nil, err
	}
	var copied []string
	var errs []error
	for _, f := range resultFiles {
		ok, err := extractOne(ctx, e, disk, filepath.Join(result, f.name), "/"+f.name, f.cap)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.name, err))
			continue
		}
		if ok {
			copied = append(copied, f.name)
		}
	}
	return copied, errors.Join(errs...)
}

// extractOne copies one file off the disk into dst, removing dst again when
// the file turns out missing or over its cap.
func extractOne(ctx context.Context, e Env, disk, dst, src string, limit int64) (bool, error) {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return false, err
	}
	w := &capped{w: out, left: limit}
	found, err := debugfsCat(ctx, e, disk, src, w)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if w.over {
		err = ErrTooLarge
	}
	if err != nil || !found {
		_ = os.Remove(dst)
		return false, err
	}
	return true, os.Chown(dst, e.ResultUID, e.ResultGID)
}

// debugfsCat streams one file from the disk into w. debugfs exits 0 even
// for a missing file and only complains on stderr, so any stderr past its
// version banner means "not found".
func debugfsCat(ctx context.Context, e Env, disk, path string, w io.Writer) (bool, error) {
	var stderr bytes.Buffer
	res, err := e.Run.Run(ctx, system.Cmd{
		Name: "setpriv",
		Args: []string{"--reuid=" + nobody, "--regid=" + nobody, "--clear-groups", "--no-new-privs",
			"--inh-caps=-all", "--bounding-set=-all", "--", "debugfs", "-R", "cat " + path, disk},
		CleanEnv: true,
		Stdout:   w,
		Stderr:   &capped{w: &stderr, left: 4096},
	})
	if c, ok := w.(*capped); ok && c.over {
		// Past the cap, debugfs dies of SIGPIPE; the caller sees c.over.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if res.Code != 0 {
		return false, fmt.Errorf("debugfs exit %d: %s", res.Code, strings.TrimSpace(stderr.String()))
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if line != "" && !strings.HasPrefix(line, "debugfs ") {
			return false, nil
		}
	}
	return true, nil
}

// capped passes at most left bytes to w, then fails every write.
type capped struct {
	w    io.Writer
	left int64
	over bool
}

func (c *capped) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.over = true
		return 0, ErrTooLarge
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

// Tidy removes everything from the run folder but the results and, after a
// build, the built root disk.
func Tidy(e Env, job Job) error {
	dir := job.ID.Dir(e.Paths)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, en := range entries {
		name := en.Name()
		if name == "result" || (job.Config.Mode == guestinit.ModeBuild && name == "rootfs.ext4") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RemoveRun deletes a run folder once the CLI has read its results. Gone
// already counts as done.
func RemoveRun(e Env, id RunID) error {
	dir := id.Dir(e.Paths)
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !fi.IsDir() || !ok || int(st.Uid) != e.Owner {
		return fmt.Errorf("%s: not a run folder", dir)
	}
	return os.RemoveAll(dir)
}
