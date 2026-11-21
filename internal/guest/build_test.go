package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeSources writes stand-ins for the pinned downloads.
type fakeSources struct {
	plugin []byte
	fail   string // which fetch fails
	calls  []string
}

var (
	kernelBytes = []byte("\x7fELF pinned kernel")
	baseBytes   = []byte("hsqs pinned base")
)

func (s *fakeSources) put(name, dest string, b []byte) error {
	s.calls = append(s.calls, name)
	if s.fail == name {
		return errors.New(name + ": network down")
	}
	return os.WriteFile(dest, b, 0o644)
}

func (s *fakeSources) Kernel(_ context.Context, dest string) error {
	return s.put("kernel", dest, kernelBytes)
}

func (s *fakeSources) Base(_ context.Context, dest string) error {
	return s.put("base", dest, baseBytes)
}

func (s *fakeSources) Claude(_ context.Context, dest string) error {
	return s.put("claude", dest, []byte("\x7fELF claude"))
}

func (s *fakeSources) Plugin(context.Context) ([]byte, error) {
	s.calls = append(s.calls, "plugin")
	if s.fail == "plugin" {
		return nil, errors.New("plugin: network down")
	}
	return s.plugin, nil
}

// fakeLauncher stands in for cc-launch: a build leaves rootfs.ext4 in the
// run folder, as the real one does.
type fakeLauncher struct {
	paths  launch.Paths
	rootfs []byte
	err    error
	calls  []string
}

func (l *fakeLauncher) Build(_ context.Context, id launch.RunID) error {
	l.calls = append(l.calls, "build "+id.String())
	if l.err != nil {
		return l.err
	}
	dir := id.Dir(l.paths)
	os.MkdirAll(dir, 0o755)
	return os.WriteFile(filepath.Join(dir, "rootfs.ext4"), l.rootfs, 0o444)
}

func (l *fakeLauncher) Clean(_ context.Context, id launch.RunID) error {
	l.calls = append(l.calls, "clean "+id.String())
	return nil
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func buildRig(t *testing.T) (*Builder, *fakeSources, *fakeLauncher, *system.FakeRunner) {
	t.Helper()
	if _, err := lookTool("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4 here")
	}
	root := t.TempDir()
	p := launch.DefaultPaths()
	for _, f := range []*string{&p.StateDir, &p.RunsDir, &p.ImagesSum, &p.Kernel, &p.RootDisk, &p.RulesHash,
		&p.BinDir, &p.Jailer, &p.Firecracker, &p.JailerBase, &p.LockPath, &p.CgroupRoot} {
		*f = filepath.Join(root, *f)
	}
	initBin := filepath.Join(root, "cc-guestinit")
	os.WriteFile(initBin, []byte("\x7fELF init"), 0o755)
	ca := filepath.Join(root, "ca.crt")
	os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\n"), 0o644)

	src := &fakeSources{plugin: goodPlugin(t)}
	l := &fakeLauncher{paths: p, rootfs: []byte("built root disk")}
	priv := &system.FakeRunner{}
	b := &Builder{
		Sources: src, Run: system.ExecRunner{}, Root: priv, Launch: l,
		Cache: filepath.Join(root, "cache"), GuestInit: initBin, CACert: ca, Paths: p,
		Now: func() time.Time { return time.Date(2026, 11, 21, 10, 15, 0, 0, time.UTC) },
	}
	return b, src, l, priv
}

func scriptInstall(priv *system.FakeRunner) {
	for range 7 { // two folders, five run files
		priv.On("install").Return(system.Result{})
	}
	priv.On("mv").Return(system.Result{})
	priv.On("install").Return(system.Result{}) // the kernel
	for range 2 {                              // images.sha256 and the stamp
		priv.On("install").Return(system.Result{})
		priv.On("dd").Return(system.Result{})
	}
}

func argvs(r *system.FakeRunner) []string {
	var out []string
	for _, c := range r.Calls() {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

func TestBuild(t *testing.T) {
	b, src, l, priv := buildRig(t)
	scriptInstall(priv)
	stamp, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !slices.Equal(src.calls, []string{"kernel", "base", "claude", "plugin"}) {
		t.Errorf("fetched %v", src.calls)
	}
	if len(l.calls) != 2 || !strings.HasPrefix(l.calls[0], "build cc-20261121-101500-") || !strings.HasPrefix(l.calls[1], "clean ") {
		t.Fatalf("launcher calls = %v", l.calls)
	}
	id, _ := launch.ParseRunID(strings.TrimPrefix(l.calls[0], "build "))
	run := id.Dir(b.Paths)

	calls := priv.Calls()
	for _, c := range calls {
		if !c.Root {
			t.Errorf("%s ran without the privilege tool", c.Name)
		}
	}
	got := argvs(priv)
	want := []string{
		"install -d -m 0755 -o root -g root " + b.Paths.RunsDir,
		"install -d -m 0755 -o root -g root " + run,
	}
	for _, f := range []string{"config.img", "vmlinux", "init.cpio", "base.squashfs", "inputs.ext4"} {
		want = append(want, "install -m 0644 -o root -g root -T "+filepath.Join(b.Cache, f)+" "+filepath.Join(run, f))
	}
	want = append(want,
		"mv -f -T "+filepath.Join(run, "rootfs.ext4")+" "+b.Paths.RootDisk,
		"install -m 0644 -o root -g root -T "+filepath.Join(b.Cache, "vmlinux")+" "+b.Paths.Kernel,
	)
	if !slices.Equal(got[:len(want)], want) {
		t.Errorf("root commands:\n%s\nwant first:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// images.sha256 lists exactly the kernel and the built disk.
	var sums, stampJSON string
	for i, c := range calls {
		if c.Name == "dd" && strings.Contains(c.Args[0], "images.sha256") {
			sums = string(c.Stdin)
		}
		if c.Name == "dd" && strings.Contains(c.Args[0], "build.json") {
			stampJSON = string(c.Stdin)
			if calls[i-1].Args[1] != "0644" {
				t.Errorf("stamp mode %s", calls[i-1].Args[1])
			}
		}
	}
	wantSums := hexSum(kernelBytes) + "  " + b.Paths.Kernel + "\n" + hexSum([]byte("built root disk")) + "  " + b.Paths.RootDisk + "\n"
	if sums != wantSums {
		t.Errorf("images.sha256 =\n%s\nwant\n%s", sums, wantSums)
	}
	var written Stamp
	if err := json.Unmarshal([]byte(stampJSON), &written); err != nil || written != stamp || stamp.RootDisk != hexSum([]byte("built root disk")) {
		t.Errorf("stamp written %q (%v), returned %+v", stampJSON, err, stamp)
	}
	want2, _ := WantStamp(b.GuestInit, b.CACert)
	want2.RootDisk = stamp.RootDisk
	if stamp != want2 {
		t.Errorf("stamp %+v, want %+v", stamp, want2)
	}

	// The builder's files: a reproducible initramfs and a build config.
	cpio, _ := os.ReadFile(filepath.Join(b.Cache, "init.cpio"))
	if init, _ := os.ReadFile(b.GuestInit); string(cpio) != string(Initramfs(init)) {
		t.Error("init.cpio doesn't hold cc-guestinit")
	}
	if cfg, _ := os.ReadFile(filepath.Join(b.Cache, "config.img")); !strings.HasPrefix(string(cfg), "mode=build\nrun_id="+id.String()+"\n") {
		t.Errorf("config.img starts %q", cfg[:40])
	}
}

func TestBuildStopsBeforeRootWhenAFetchFails(t *testing.T) {
	b, src, l, priv := buildRig(t)
	src.fail = "claude"
	if _, err := b.Build(context.Background()); err == nil {
		t.Fatal("Build carried on without Claude Code")
	}
	if len(priv.Calls()) != 0 || len(l.calls) != 0 {
		t.Errorf("root commands %v, launcher %v after a failed fetch", argvs(priv), l.calls)
	}
}

func TestBuildCleansUpAFailedBoot(t *testing.T) {
	b, _, l, priv := buildRig(t)
	for range 7 {
		priv.On("install").Return(system.Result{})
	}
	l.err = &launch.ExitError{Code: launch.ExitTimeout}
	_, err := b.Build(context.Background())
	var ee *launch.ExitError
	if !errors.As(err, &ee) || ee.Code != launch.ExitTimeout {
		t.Fatalf("Build = %v, want the launcher's error", err)
	}
	if len(l.calls) != 2 || !strings.HasPrefix(l.calls[1], "clean ") {
		t.Errorf("launcher calls = %v; want the run cleaned", l.calls)
	}
	for _, a := range argvs(priv) {
		if strings.HasPrefix(a, "mv ") {
			t.Errorf("a failed build installed its disk: %s", a)
		}
	}
}

func TestWantStampFollowsTheInputs(t *testing.T) {
	b, _, _, _ := buildRig(t)
	a, err := WantStamp(b.GuestInit, b.CACert)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(b.CACert, []byte("-----BEGIN CERTIFICATE-----\nnew CA\n"), 0o644)
	c, _ := WantStamp(b.GuestInit, b.CACert)
	if a == c {
		t.Error("a new CA left the stamp unchanged; the guest must rebuild to trust it")
	}
}
