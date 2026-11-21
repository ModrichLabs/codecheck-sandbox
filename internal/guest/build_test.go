package guest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeSources writes stand-ins for the pinned downloads.
type fakeSources struct {
	plugin []byte
	kernel []byte
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
	k := kernelBytes
	if s.kernel != nil {
		k = s.kernel
	}
	return s.put("kernel", dest, k)
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
	return os.WriteFile(filepath.Join(id.Dir(l.paths), "rootfs.ext4"), l.rootfs, 0o444)
}

func (l *fakeLauncher) Clean(_ context.Context, id launch.RunID) error {
	l.calls = append(l.calls, "clean "+id.String())
	return nil
}

// rootRunner plays the root commands Build uses, for real, inside the test's
// temp tree: install, mv, dd and rm. failOn makes the first call whose argv
// contains it fail.
type rootRunner struct {
	failOn string
	calls  []system.Cmd
}

func (r *rootRunner) Run(_ context.Context, c system.Cmd) (system.Result, error) {
	r.calls = append(r.calls, c)
	argv := strings.Join(append([]string{c.Name}, c.Args...), " ")
	if r.failOn != "" && strings.Contains(argv, r.failOn) {
		r.failOn = ""
		return system.Result{Code: 1, Stderr: []byte("injected failure")}, nil
	}
	fail := func(err error) (system.Result, error) {
		return system.Result{Code: 1, Stderr: []byte(err.Error())}, nil
	}
	a := c.Args
	switch c.Name {
	case "install":
		if a[0] == "-d" {
			if err := os.MkdirAll(a[len(a)-1], 0o755); err != nil {
				return fail(err)
			}
			return system.Result{}, nil
		}
		mode, _ := strconv.ParseUint(a[slices.Index(a, "-m")+1], 8, 32)
		src, dst := a[len(a)-2], a[len(a)-1]
		in, err := os.Open(src)
		if err != nil {
			return fail(err)
		}
		defer in.Close()
		os.Remove(dst)
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(mode))
		if err != nil {
			return fail(err)
		}
		io.Copy(out, in)
		out.Close()
	case "mv":
		if err := os.Rename(a[len(a)-2], a[len(a)-1]); err != nil {
			return fail(err)
		}
	case "dd":
		dst := strings.TrimPrefix(a[0], "of=")
		if err := os.WriteFile(dst, c.Stdin, 0o644); err != nil {
			return fail(err)
		}
	case "rm":
		for _, p := range a[2:] {
			os.Remove(p)
		}
	default:
		return fail(fmt.Errorf("unexpected root command %s", c.Name))
	}
	return system.Result{}, nil
}

func (r *rootRunner) argvs() []string {
	var out []string
	for _, c := range r.calls {
		out = append(out, strings.Join(append([]string{c.Name}, c.Args...), " "))
	}
	return out
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func buildRig(t *testing.T) (*Builder, *fakeSources, *fakeLauncher, *rootRunner) {
	t.Helper()
	if _, err := system.LookTool("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4 here")
	}
	// The test kernel stands in for the pinned one.
	saved := buildPins
	buildPins.Kernel = hexSum(kernelBytes)
	t.Cleanup(func() { buildPins = saved })

	root := t.TempDir()
	p := launch.DefaultPaths()
	for _, f := range []*string{&p.StateDir, &p.RunsDir, &p.ImagesSum, &p.Kernel, &p.RootDisk, &p.RulesHash,
		&p.BinDir, &p.Jailer, &p.Firecracker, &p.JailerBase, &p.LockPath, &p.CgroupRoot} {
		*f = filepath.Join(root, *f)
	}
	os.MkdirAll(p.StateDir, 0o755)
	initBin := filepath.Join(root, "cc-guestinit")
	os.WriteFile(initBin, []byte("\x7fELF init"), 0o755)
	ca := filepath.Join(root, "ca.crt")
	os.WriteFile(ca, []byte("-----BEGIN CERTIFICATE-----\n"), 0o644)

	src := &fakeSources{plugin: goodPlugin(t)}
	l := &fakeLauncher{paths: p, rootfs: []byte("built root disk")}
	priv := &rootRunner{}
	b := &Builder{
		Sources: src, Run: system.ExecRunner{}, Root: priv, Launch: l,
		Cache: filepath.Join(root, "cache"), GuestInit: initBin, CACert: ca, Paths: p,
		Now: func() time.Time { return time.Date(2026, 11, 21, 10, 15, 0, 0, time.UTC) },
	}
	return b, src, l, priv
}

// leftoverNew lists *.new files left in the state folder.
func leftoverNew(t *testing.T, p launch.Paths) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(p.StateDir, "*.new"))
	return m
}

func TestBuild(t *testing.T) {
	b, src, l, priv := buildRig(t)
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

	for _, c := range priv.calls {
		if !c.Root {
			t.Errorf("%s ran without the privilege tool", c.Name)
		}
	}
	want := []string{
		"install -d -m 0755 -o root -g root " + b.Paths.RunsDir,
		"install -d -m 0755 -o root -g root " + run,
	}
	for _, f := range []string{"config.img", "vmlinux", "init.cpio", "base.squashfs", "inputs.ext4"} {
		want = append(want, "install -m 0644 -o root -g root -T "+filepath.Join(b.Cache, f)+" "+filepath.Join(run, f))
	}
	// Everything new lands beside the old image first; the swap comes last.
	want = append(want,
		"mv -f -T "+filepath.Join(run, "rootfs.ext4")+" "+b.Paths.RootDisk+".new",
		"install -m 0644 -o root -g root -T "+filepath.Join(b.Cache, "vmlinux")+" "+b.Paths.Kernel+".new",
	)
	got := priv.argvs()
	if !slices.Equal(got[:len(want)], want) {
		t.Errorf("root commands:\n%s\nwant first:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	wantSwap := []string{
		"mv -f -T " + b.Paths.RootDisk + ".new " + b.Paths.RootDisk,
		"mv -f -T " + b.Paths.Kernel + ".new " + b.Paths.Kernel,
		"mv -f -T " + b.Paths.ImagesSum + ".new " + b.Paths.ImagesSum,
		"mv -f -T " + StampPath(b.Paths) + ".new " + StampPath(b.Paths),
	}
	if !slices.Equal(got[len(got)-4:], wantSwap) {
		t.Errorf("the last commands should swap the four files in:\n%s", strings.Join(got[len(got)-4:], "\n"))
	}

	sums, _ := os.ReadFile(b.Paths.ImagesSum)
	wantSums := hexSum(kernelBytes) + "  " + b.Paths.Kernel + "\n" + hexSum([]byte("built root disk")) + "  " + b.Paths.RootDisk + "\n"
	if string(sums) != wantSums {
		t.Errorf("images.sha256 =\n%s\nwant\n%s", sums, wantSums)
	}
	var written Stamp
	js, _ := os.ReadFile(StampPath(b.Paths))
	if err := json.Unmarshal(js, &written); err != nil || written != stamp || stamp.RootDisk != hexSum([]byte("built root disk")) {
		t.Errorf("stamp written %s (%v), returned %+v", js, err, stamp)
	}
	if k, _ := os.ReadFile(b.Paths.Kernel); !bytes.Equal(k, kernelBytes) {
		t.Error("the kernel didn't land")
	}
	if left := leftoverNew(t, b.Paths); len(left) != 0 {
		t.Errorf("left %v behind", left)
	}

	cpio, _ := os.ReadFile(filepath.Join(b.Cache, "init.cpio"))
	if init, _ := os.ReadFile(b.GuestInit); string(cpio) != string(Initramfs(init)) {
		t.Error("init.cpio doesn't hold cc-guestinit")
	}
	if cfg, _ := os.ReadFile(filepath.Join(b.Cache, "config.img")); !strings.HasPrefix(string(cfg), "mode=build\nrun_id="+id.String()+"\n") {
		t.Errorf("config.img starts %q", cfg[:40])
	}
}

// oldImage installs a consistent earlier image, as a rebuild finds it.
func oldImage(t *testing.T, b *Builder) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		b.Paths.Kernel:   kernelBytes,
		b.Paths.RootDisk: []byte("old root disk"),
	}
	files[b.Paths.ImagesSum] = []byte(hexSum(kernelBytes) + "  " + b.Paths.Kernel + "\n" + hexSum([]byte("old root disk")) + "  " + b.Paths.RootDisk + "\n")
	files[StampPath(b.Paths)] = []byte(`{"version":1}`)
	for p, c := range files {
		os.WriteFile(p, c, 0o644)
	}
	return files
}

func TestBuildKeepsTheOldImageWhenTheInstallFails(t *testing.T) {
	for _, failOn := range []string{"vmlinux.new", "of=" + "IMAGES", "of=" + "STAMP"} {
		t.Run(failOn, func(t *testing.T) {
			b, _, l, priv := buildRig(t)
			before := oldImage(t, b)
			priv.failOn = strings.NewReplacer("IMAGES", b.Paths.ImagesSum+".new", "STAMP", StampPath(b.Paths)+".new").Replace(failOn)
			if _, err := b.Build(context.Background()); err == nil {
				t.Fatal("Build reported success")
			}
			for p, want := range before {
				if got, _ := os.ReadFile(p); !bytes.Equal(got, want) {
					t.Errorf("%s changed to %q; a failed rebuild must leave the old image whole", filepath.Base(p), got)
				}
			}
			if left := leftoverNew(t, b.Paths); len(left) != 0 {
				t.Errorf("left %v behind", left)
			}
			if !strings.HasPrefix(l.calls[len(l.calls)-1], "clean ") {
				t.Errorf("the run never got cleaned: %v", l.calls)
			}
		})
	}
}

func TestBuildRefusesAKernelOffItsPin(t *testing.T) {
	b, src, _, _ := buildRig(t)
	before := oldImage(t, b)
	src.kernel = []byte("\x7fELF some other kernel")
	if _, err := b.Build(context.Background()); err == nil || !strings.Contains(err.Error(), "pin") {
		t.Fatalf("Build = %v, want a refusal naming the pin", err)
	}
	if got, _ := os.ReadFile(b.Paths.Kernel); !bytes.Equal(got, before[b.Paths.Kernel]) {
		t.Error("an unpinned kernel got installed")
	}
	if left := leftoverNew(t, b.Paths); len(left) != 0 {
		t.Errorf("left %v behind", left)
	}
}

func TestBuildStopsBeforeRootWhenAFetchFails(t *testing.T) {
	b, src, l, priv := buildRig(t)
	src.fail = "claude"
	if _, err := b.Build(context.Background()); err == nil {
		t.Fatal("Build carried on without Claude Code")
	}
	if len(priv.calls) != 0 || len(l.calls) != 0 {
		t.Errorf("root commands %v, launcher %v after a failed fetch", priv.argvs(), l.calls)
	}
}

func TestBuildCleansUpAFailedBoot(t *testing.T) {
	b, _, l, priv := buildRig(t)
	l.err = &launch.ExitError{Code: launch.ExitTimeout}
	_, err := b.Build(context.Background())
	var ee *launch.ExitError
	if !errors.As(err, &ee) || ee.Code != launch.ExitTimeout {
		t.Fatalf("Build = %v, want the launcher's error", err)
	}
	if len(l.calls) != 2 || !strings.HasPrefix(l.calls[1], "clean ") {
		t.Errorf("launcher calls = %v; want the run cleaned", l.calls)
	}
	for _, a := range priv.argvs() {
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
