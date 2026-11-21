package launch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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

// afterResultWrite lets tests act in the window after a result file closes.
var afterResultWrite func(path string)

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
//
// Firecracker, which runs as the jail user, can write where the disk sits,
// so every change goes through a descriptor opened without following links,
// and the file that arrives must be the inode that descriptor holds.
func CollectOutDisk(e Env, j *Jail, job Job) error {
	dst := filepath.Join(job.ID.Dir(e.Paths), diskName(job.Config.Mode))
	f, err := os.OpenFile(j.OutDisk, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", j.OutDisk, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || !ok || st.Nlink != 1 {
		return fmt.Errorf("%s: not a plain file with one link", j.OutDisk)
	}
	if err := f.Chown(e.Owner, e.Owner); err != nil {
		return err
	}
	if err := f.Chmod(0o444); err != nil {
		return err
	}

	err = os.Rename(j.OutDisk, dst)
	if errors.Is(err, unix.EXDEV) {
		// Another filesystem: copy from the checked descriptor itself.
		return copyFrom(f, dst, 0o444)
	}
	if err != nil {
		return err
	}
	// The path could have changed between the open and the rename.
	got, err := os.Lstat(dst)
	if err != nil {
		return err
	}
	if !os.SameFile(fi, got) {
		_ = os.Remove(dst)
		return fmt.Errorf("%s changed while it moved", j.OutDisk)
	}
	return nil
}

// copyFrom copies an open file into a new file dst with mode.
func copyFrom(in *os.File, dst string, mode os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Chmod(mode); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Extract reads the run's writable disk with debugfs as nobody. For a job it
// copies the fixed file list, each within its cap, into a result folder the
// calling user owns, and returns the names it copied; a missing file just
// stays missing. For a build it checks the completion marker instead.
func Extract(ctx context.Context, e Env, job Job) (copied []string, err error) {
	disk := filepath.Join(job.ID.Dir(e.Paths), diskName(job.Config.Mode))
	if job.Config.Mode == guestinit.ModeBuild {
		var buf bytes.Buffer
		found, err := debugfsCat(ctx, e, disk, buildMarker, &capped{w: &buf, left: 128})
		if err != nil || !found || strings.TrimSpace(buf.String()) != job.ID.String() {
			return nil, errors.Join(ErrBuildIncomplete, err)
		}
		return nil, nil
	}

	// The folder stays root's until every file sits in it, owned through its
	// descriptor: a user who owned the folder sooner could swap a file for a
	// symlink between its write and any later call by path.
	result := filepath.Join(job.ID.Dir(e.Paths), "result")
	if err := os.Mkdir(result, 0o700); err != nil {
		return nil, err
	}
	defer func() {
		if cerr := os.Lchown(result, e.ResultUID, e.ResultGID); cerr != nil && err == nil {
			err = cerr
		}
	}()
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
	if !job.Started.IsZero() {
		if err := copyDenials(e, job.Started, filepath.Join(result, "proxy-denials.jsonl")); err != nil {
			errs = append(errs, fmt.Errorf("proxy denials: %w", err))
		}
	}
	return copied, errors.Join(errs...)
}

// maxDenials caps how many of a run's proxy denials get copied.
const maxDenials = 1000

// denial mirrors proxy.Denial: cc-launch can't import the proxy package,
// which pulls in net/http.
type denial struct {
	Time   time.Time `json:"time"`
	Reason string    `json:"reason"`
	Client string    `json:"client,omitempty"`
	Method string    `json:"method,omitempty"`
	Host   string    `json:"host,omitempty"`
	Path   string    `json:"path,omitempty"`
}

// copyDenials writes the proxy denials logged since the boot to dst, in the
// still root-owned result folder: one VM runs at a time, so they all belong
// to this run. It reads the previous rotation too, re-encodes each entry
// (dropping anything that doesn't parse), and keeps maxDenials; a last line,
// {"dropped":N}, counts the rest. An absent log means the proxy denied
// nothing. On any failure dst goes, so no root-only file stays to break the
// caller's report.
func copyDenials(e Env, since time.Time, dst string) (err error) {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
		}
	}()
	kept, dropped := 0, 0
	for _, p := range []string{e.Paths.DenyLog + ".1", e.Paths.DenyLog} {
		if err := readDenials(p, since, func(d denial) error {
			if kept >= maxDenials {
				dropped++
				return nil
			}
			kept++
			line, _ := json.Marshal(d)
			_, err := out.Write(append(line, '\n'))
			return err
		}); err != nil {
			return err
		}
	}
	if dropped > 0 {
		if _, err := fmt.Fprintf(out, "{\"dropped\":%d}\n", dropped); err != nil {
			return err
		}
	}
	return out.Chown(e.ResultUID, e.ResultGID)
}

// readDenials hands each parsable entry of one log file stamped at or after
// since to fn; an absent file holds none.
func readDenials(path string, since time.Time, fn func(denial) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 16<<20))
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		var d denial
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.Time.Before(since) {
			continue
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return nil
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
	if w.over {
		err = ErrTooLarge
	}
	keep := err == nil && found
	if keep {
		// Through the descriptor, never by path.
		err = out.Chown(e.ResultUID, e.ResultGID)
		keep = err == nil
	}
	if cerr := out.Close(); err == nil {
		err = cerr
		keep = keep && cerr == nil
	}
	if afterResultWrite != nil {
		afterResultWrite(dst)
	}
	if !keep {
		_ = os.Remove(dst) // the folder is still root's, so dst is still ours
		return false, err
	}
	return true, nil
}

// notFound ends the line debugfs prints for a missing file.
const notFound = ": File not found by ext2_lookup"

// debugfsCat streams one file from the disk into w. Root opens the disk and
// hands it to debugfs as stdin, so whether nobody may enter the run folder
// never matters. debugfs exits 0 whatever happens and reports on stderr: a
// "File not found" line means the file is missing, and any other line past
// the version banner (such as a failure to open the disk) is an error.
func debugfsCat(ctx context.Context, e Env, disk, path string, w io.Writer) (bool, error) {
	f, err := os.OpenFile(disk, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var stderr bytes.Buffer
	res, err := e.Run.Run(ctx, system.Cmd{
		Name: "setpriv",
		Args: []string{"--reuid=" + nobody, "--regid=" + nobody, "--clear-groups", "--no-new-privs",
			"--inh-caps=-all", "--bounding-set=-all", "--", "debugfs", "-R", "cat " + path, "/dev/stdin"},
		CleanEnv:  true,
		StdinFile: f,
		Stdout:    w,
		Stderr:    &capped{w: &stderr, left: 4096},
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
	missing := false
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "debugfs "): // the version banner
		case strings.HasSuffix(line, notFound):
			missing = true
		default:
			return false, fmt.Errorf("debugfs: %s", line)
		}
	}
	return !missing, nil
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
