package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// diskRunner plays debugfs over an imaginary output disk: files maps a path
// on the disk to its contents, and a missing path makes debugfs complain on
// stderr while still exiting 0, as the real one does.
type diskRunner struct {
	files    map[string]string
	calls    []system.Cmd
	disks    []string // the files handed over as stdin
	openFail bool     // debugfs can't open the disk, and still exits 0
}

func (d *diskRunner) Run(_ context.Context, c system.Cmd) (system.Result, error) {
	d.calls = append(d.calls, c)
	if c.StdinFile != nil {
		d.disks = append(d.disks, c.StdinFile.Name())
	}
	if d.openFail {
		msg := "debugfs 1.47.4 (6-Mar-2025)\ndebugfs: Permission denied while trying to open /dev/stdin\ncat: Filesystem not open\n"
		c.Stderr.Write([]byte(msg))
		return system.Result{}, nil
	}
	req := c.Args[slices.Index(c.Args, "-R")+1] // "cat /name"
	content, ok := d.files[strings.TrimPrefix(req, "cat ")]
	if !ok {
		msg := "debugfs 1.47.2\n" + req + ": File not found by ext2_lookup \n"
		if c.Stderr != nil {
			c.Stderr.Write([]byte(msg))
			return system.Result{}, nil
		}
		return system.Result{Stderr: []byte(msg)}, nil
	}
	if _, err := c.Stdout.Write([]byte(content)); err != nil {
		return system.Result{}, err
	}
	return system.Result{}, nil
}

func extractRig(t *testing.T, cfg guestinit.Config, files map[string]string) (Env, Job, *diskRunner, string) {
	t.Helper()
	r := newRig(t, cfg)
	d := &diskRunner{files: files}
	r.env.Run = d
	r.env.ResultUID, r.env.ResultGID = os.Getuid(), os.Getgid()
	disk := filepath.Join(r.dir, "out.ext4")
	if cfg.Mode == guestinit.ModeBuild {
		disk = filepath.Join(r.dir, "rootfs.ext4")
	}
	write(t, disk, ext4Image(4096), 0o444)
	return r.env, Job{ID: r.id, Config: cfg}, d, r.dir
}

func TestExtractCopiesTheFixedList(t *testing.T) {
	e, job, d, dir := extractRig(t, reviewCfg(), map[string]string{
		"/stream.jsonl": `{"type":"result"}` + "\n",
		"/stderr.log":   "",
		"/exit-code":    "0\n",
		"/etc/passwd":   "never asked for",
	})
	got, err := Extract(context.Background(), e, job)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !slices.Equal(got, []string{"stream.jsonl", "stderr.log", "exit-code"}) {
		t.Errorf("Extract copied %v", got)
	}
	result := filepath.Join(dir, "result")
	if b, _ := os.ReadFile(filepath.Join(result, "exit-code")); string(b) != "0\n" {
		t.Errorf("exit-code = %q", b)
	}
	if fi, err := os.Stat(result); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("result folder: %v, %v; want 0700", fi, err)
	}
	if fi, _ := os.Stat(filepath.Join(result, "stream.jsonl")); fi.Mode().Perm() != 0o600 {
		t.Errorf("stream.jsonl mode %v, want 0600", fi.Mode().Perm())
	}

	// debugfs never runs as root: it parses a disk the guest wrote. Root
	// opens the disk and hands it over as stdin, so the run folder's
	// permissions never decide whether nobody can read it.
	for i, c := range d.calls {
		want := []string{"--reuid=65534", "--regid=65534", "--clear-groups", "--no-new-privs",
			"--inh-caps=-all", "--bounding-set=-all", "--", "debugfs", "-R"}
		if c.Name != "setpriv" || !slices.Equal(c.Args[:len(want)], want) || c.Args[len(c.Args)-1] != "/dev/stdin" || c.Root {
			t.Errorf("call = %s %v", c.Name, c.Args)
		}
		if i >= len(d.disks) || d.disks[i] != filepath.Join(dir, "out.ext4") {
			t.Errorf("call %d got stdin %v, want the output disk", i, d.disks)
		}
	}
}

func TestExtractFailsWhenDebugfsCantOpenTheDisk(t *testing.T) {
	e, job, d, _ := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	d.openFail = true
	if got, err := Extract(context.Background(), e, job); err == nil {
		t.Errorf("Extract = %v, nil; an unreadable disk must not pass for missing files", got)
	}
}

func TestExtractSkipsMissingFiles(t *testing.T) {
	// A job that never finished leaves no exit-code; the caller decides.
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/stream.jsonl": "{}\n"})
	got, err := Extract(context.Background(), e, job)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !slices.Equal(got, []string{"stream.jsonl"}) {
		t.Errorf("Extract copied %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "result", "exit-code")); !os.IsNotExist(err) {
		t.Errorf("an empty exit-code appeared: %v", err)
	}
}

func TestExtractCapsEachFile(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{
		"/stream.jsonl": "{}\n",
		"/exit-code":    strings.Repeat("9", 17),
	})
	got, err := Extract(context.Background(), e, job)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Extract = %v, want ErrTooLarge", err)
	}
	if slices.Contains(got, "exit-code") {
		t.Error("Extract reported the oversize file as copied")
	}
	if _, err := os.Stat(filepath.Join(dir, "result", "exit-code")); !os.IsNotExist(err) {
		t.Errorf("a truncated exit-code stayed behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "result", "stream.jsonl")); err != nil {
		t.Errorf("the files within their caps went too: %v", err)
	}
}

// A real process writing past the cap ends promptly (SIGPIPE) and the
// overflow shows, rather than hanging on a full pipe.
func TestCappedWriterStopsARealProcess(t *testing.T) {
	var sink strings.Builder
	w := &capped{w: &sink, left: 10}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := system.ExecRunner{}.Run(ctx, system.Cmd{Name: "head", Args: []string{"-c", "1000000", "/dev/zero"}, Stdout: w})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !w.over || sink.Len() != 0 {
		t.Errorf("over %v, kept %d bytes; want the overflow flagged and nothing past the cap", w.over, sink.Len())
	}
}

func TestExtractRefusesAPlantedResultFolder(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "result")); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(context.Background(), e, job); err == nil {
		t.Error("Extract wrote through a symlinked result folder")
	}
}

func TestExtractBuildChecksTheMarker(t *testing.T) {
	cfg := guestinit.Config{Mode: guestinit.ModeBuild, RunID: "cc-20261121-101500-a1b2c3"}
	e, job, d, _ := extractRig(t, cfg, map[string]string{
		"/etc/codecheck-sandbox/build-complete": "cc-20261121-101500-a1b2c3\n",
	})
	if _, err := Extract(context.Background(), e, job); err != nil {
		t.Fatalf("Extract build: %v", err)
	}
	if len(d.calls) != 1 || len(d.disks) != 1 || !strings.HasSuffix(d.disks[0], "rootfs.ext4") {
		t.Errorf("calls = %+v, disks %v; want one read of rootfs.ext4", d.calls, d.disks)
	}

	for name, marker := range map[string]map[string]string{
		"no marker": {},
		"wrong run": {"/etc/codecheck-sandbox/build-complete": "cc-20261121-101500-zzzzzz\n"},
	} {
		t.Run(name, func(t *testing.T) {
			e, job, _, _ := extractRig(t, cfg, marker)
			if _, err := Extract(context.Background(), e, job); !errors.Is(err, ErrBuildIncomplete) {
				t.Errorf("Extract = %v, want ErrBuildIncomplete", err)
			}
		})
	}
}

func TestCollectOutDiskMovesItOutOfTheJail(t *testing.T) {
	r, m, job := stageRig(t, reviewCfg())
	j, err := Stage(context.Background(), r.env, m, job)
	if err != nil {
		t.Fatal(err)
	}
	if err := CollectOutDisk(r.env, j, job); err != nil {
		t.Fatalf("CollectOutDisk: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(r.dir, "out.ext4"))
	if err != nil || fi.Mode().Perm() != 0o444 || fi.Size() != OutDiskSize {
		t.Errorf("out.ext4 in the run folder: %v, %v; want 0444 and full size", fi, err)
	}
	if _, err := os.Lstat(j.OutDisk); !os.IsNotExist(err) {
		t.Errorf("the disk stayed in the jail: %v", err)
	}
}

// The jail's disk path sits where Firecracker can write: a symlink planted
// there must not turn root's chmod and chown onto another file.
func TestCollectOutDiskRefusesASymlink(t *testing.T) {
	r, m, job := stageRig(t, reviewCfg())
	j, err := Stage(context.Background(), r.env, m, job)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "shadow")
	write(t, victim, []byte("secret"), 0o600)
	os.Remove(j.OutDisk)
	if err := os.Symlink(victim, j.OutDisk); err != nil {
		t.Fatal(err)
	}
	if err := CollectOutDisk(r.env, j, job); err == nil {
		t.Error("CollectOutDisk accepted a symlink")
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Errorf("the symlink's target changed mode to %v", fi.Mode().Perm())
	}
	if _, err := os.Lstat(filepath.Join(r.dir, "out.ext4")); !os.IsNotExist(err) {
		t.Errorf("something landed in the run folder: %v", err)
	}
}

// Once a result file closes, nothing may touch it by path again: a user who
// owned the folder could swap it for a symlink to a root file.
func TestExtractNeverChownsByPathAfterWriting(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	defer func() { afterResultWrite = nil }()
	afterResultWrite = func(path string) {
		os.Remove(path)
		os.Symlink(filepath.Join(dir, "no-such-target"), path) // dangling: a path chown fails
	}
	if _, err := Extract(context.Background(), e, job); err != nil {
		t.Errorf("Extract = %v; it touched a result file by path after writing it", err)
	}
}

func TestExtractHandsOverTheFolderLast(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	e.ResultUID = os.Getuid() + 1 // someone else: the hand-over chown can't happen in a test
	var owners []int
	defer func() { afterResultWrite = nil }()
	afterResultWrite = func(string) {
		fi, _ := os.Stat(filepath.Join(dir, "result"))
		owners = append(owners, int(fi.Sys().(*syscall.Stat_t).Uid))
	}
	Extract(context.Background(), e, job)
	for _, o := range owners {
		if o != e.Owner {
			t.Errorf("the result folder belonged to uid %d while files went in, want %d", o, e.Owner)
		}
	}
	if len(owners) == 0 {
		t.Error("no file got written")
	}
}

func denialLine(at time.Time, host string) string {
	return fmt.Sprintf(`{"time":%q,"reason":"denied-host","client":"172.16.0.2","method":"CONNECT","host":%q}`+"\n", at.Format(time.RFC3339Nano), host)
}

func TestExtractCopiesTheRunsProxyDenials(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	e.Paths.DenyLog = filepath.Join(t.TempDir(), "proxy.jsonl")
	job.Started = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	before, during := job.Started.Add(-time.Minute), job.Started.Add(time.Minute)
	write(t, e.Paths.DenyLog+".1", []byte(denialLine(before, "old.example")+denialLine(during, "rotated.example")), 0o600)
	write(t, e.Paths.DenyLog, []byte(denialLine(during.Add(time.Second), "github.com")+"not json\n"), 0o600)

	if _, err := Extract(context.Background(), e, job); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "result", "proxy-denials.jsonl"))
	if err != nil {
		t.Fatalf("no denials file: %v", err)
	}
	got := string(b)
	for _, want := range []string{"rotated.example", "github.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("denials lack %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old.example") || strings.Contains(got, "not json") {
		t.Errorf("denials hold entries from before the run, or junk:\n%s", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("want two lines:\n%s", got)
	}
}

func TestExtractWithoutADenialLog(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	e.Paths.DenyLog = filepath.Join(t.TempDir(), "proxy.jsonl") // the proxy never denied anything
	job.Started = time.Now()
	if _, err := Extract(context.Background(), e, job); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "result", "proxy-denials.jsonl"))
	if err != nil || len(b) != 0 {
		t.Errorf("denials file = %q, %v; want an empty file", b, err)
	}
}

func TestExtractCapsTheDenials(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	e.Paths.DenyLog = filepath.Join(t.TempDir(), "proxy.jsonl")
	job.Started = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	var b strings.Builder
	for i := range maxDenials + 50 {
		b.WriteString(denialLine(job.Started.Add(time.Duration(i)*time.Millisecond), "x.example"))
	}
	write(t, e.Paths.DenyLog, []byte(b.String()), 0o600)
	if _, err := Extract(context.Background(), e, job); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "result", "proxy-denials.jsonl"))
	if n := strings.Count(string(got), "\n"); n != maxDenials {
		t.Errorf("%d lines, want the cap of %d", n, maxDenials)
	}
}

func TestTidyKeepsOnlyTheResults(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{"/exit-code": "0\n"})
	if _, err := Extract(context.Background(), e, job); err != nil {
		t.Fatal(err)
	}
	if err := Tidy(e, job); err != nil {
		t.Fatalf("Tidy: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	if !slices.Equal(names, []string{"result"}) {
		t.Errorf("run folder holds %v after Tidy, want only result", names)
	}
}

func TestRemoveRun(t *testing.T) {
	e, job, _, dir := extractRig(t, reviewCfg(), map[string]string{})
	if err := RemoveRun(e, job.ID); err != nil {
		t.Fatalf("RemoveRun: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Errorf("run folder still there: %v", err)
	}
	if err := RemoveRun(e, job.ID); err != nil {
		t.Errorf("RemoveRun on a gone run: %v", err)
	}
}
