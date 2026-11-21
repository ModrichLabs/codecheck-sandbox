package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

type stepRig struct {
	d       Deps
	priv    *system.FakeRunner
	records []step.Item
	fcBin   []byte
	jlBin   []byte
	builds  int
}

func newStepRig(t *testing.T) *stepRig {
	t.Helper()
	saved := buildPins
	buildPins.Kernel = hexSum(kernelBytes)
	t.Cleanup(func() { buildPins = saved })
	root := t.TempDir()
	p := launch.DefaultPaths()
	for _, f := range []*string{&p.StateDir, &p.RunsDir, &p.ImagesSum, &p.Kernel, &p.RootDisk, &p.RulesHash,
		&p.BinDir, &p.Jailer, &p.Firecracker, &p.JailerBase, &p.LockPath, &p.CgroupRoot} {
		*f = filepath.Join(root, *f)
	}
	initBin := filepath.Join(root, "cc-guestinit")
	os.WriteFile(initBin, []byte("\x7fELF init"), 0o755)
	ca := filepath.Join(root, "ca.crt")
	os.WriteFile(ca, []byte("CA"), 0o644)
	r := &stepRig{priv: &system.FakeRunner{}, fcBin: []byte("fc"), jlBin: []byte("jailer")}
	pin := pins.Firecracker
	pin.FirecrackerSHA256, pin.JailerSHA256 = hexSum(r.fcBin), hexSum(r.jlBin)
	r.d = Deps{
		Paths: p, Root: r.priv, Cache: filepath.Join(root, "cache"), GuestInit: initBin, CACert: ca,
		FirecrackerPin: pin,
		Record:         func(it step.Item) { r.records = append(r.records, it) },
		FetchFirecracker: func(_ context.Context, fc, jl string) error {
			os.WriteFile(fc, r.fcBin, 0o755)
			return os.WriteFile(jl, r.jlBin, 0o755)
		},
		Build: func(context.Context) (Stamp, error) {
			r.builds++
			return installImage(t, p, initBin, ca, []byte("root disk")), nil
		},
	}
	return r
}

// installImage writes what a finished build leaves behind.
func installImage(t *testing.T, p launch.Paths, initBin, ca string, rootfs []byte) Stamp {
	t.Helper()
	os.MkdirAll(p.StateDir, 0o755)
	os.WriteFile(p.Kernel, kernelBytes, 0o644)
	os.WriteFile(p.RootDisk, rootfs, 0o444)
	os.WriteFile(p.ImagesSum, []byte(fmt.Sprintf("%s  %s\n%s  %s\n", hexSum(kernelBytes), p.Kernel, hexSum(rootfs), p.RootDisk)), 0o644)
	s, err := WantStamp(initBin, ca)
	if err != nil {
		t.Fatal(err)
	}
	s.RootDisk = hexSum(rootfs)
	js, _ := json.Marshal(s)
	os.WriteFile(StampPath(p), js, 0o644)
	return s
}

func (r *stepRig) step(t *testing.T, id string) step.Step {
	t.Helper()
	for _, s := range Steps(r.d) {
		if s.ID() == id {
			return s
		}
	}
	t.Fatalf("no step %s", id)
	return nil
}

func checkOK(t *testing.T, s step.Step) step.Status {
	t.Helper()
	st, err := s.Check(context.Background())
	if err != nil {
		t.Fatalf("%s Check: %v", s.ID(), err)
	}
	return st
}

func TestStepsOrderPhaseAndTelemetry(t *testing.T) {
	var ids []string
	for _, s := range Steps(newStepRig(t).d) {
		ids = append(ids, s.ID())
		if s.Phase() != step.PhaseGuest {
			t.Errorf("%s: phase %s", s.ID(), s.Phase())
		}
		if props, _ := telemetry.Properties(telemetry.StepFinished{Step: telemetry.StepID(s.ID())}); props["step"] != s.ID() {
			t.Errorf("%s not registered with telemetry", s.ID())
		}
	}
	if !slices.Equal(ids, []string{"guest-firecracker", "guest-image"}) {
		t.Errorf("steps = %v", ids)
	}
}

func TestFirecrackerStep(t *testing.T) {
	r := newStepRig(t)
	s := r.step(t, "guest-firecracker")
	if checkOK(t, s).OK {
		t.Fatal("passed with nothing installed")
	}
	r.priv.On("install").Return(system.Result{})
	r.priv.On("install").Return(system.Result{})
	if err := s.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var got []string
	for _, c := range r.priv.Calls() {
		got = append(got, strings.Join(append([]string{c.Name}, c.Args...), " "))
		if !c.Root {
			t.Errorf("%s without the privilege tool", c.Name)
		}
	}
	want := []string{
		"install -D -m 0755 -o root -g root -T " + filepath.Join(r.d.Cache, "firecracker") + " " + r.d.Paths.Firecracker,
		"install -D -m 0755 -o root -g root -T " + filepath.Join(r.d.Cache, "jailer") + " " + r.d.Paths.Jailer,
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var paths []string
	for _, it := range r.records {
		paths = append(paths, it.Path)
	}
	if !slices.Contains(paths, r.d.Paths.Firecracker) || !slices.Contains(paths, r.d.Paths.Jailer) {
		t.Errorf("records = %v", paths)
	}

	// Installed copies matching the pins pass; a changed one fails.
	os.MkdirAll(r.d.Paths.BinDir, 0o755)
	os.WriteFile(r.d.Paths.Firecracker, r.fcBin, 0o755)
	os.WriteFile(r.d.Paths.Jailer, r.jlBin, 0o755)
	if !checkOK(t, s).OK {
		t.Error("pinned binaries failed")
	}
	os.WriteFile(r.d.Paths.Jailer, []byte("tampered"), 0o755)
	if checkOK(t, s).OK {
		t.Error("a changed jailer passed")
	}
}

func TestImageStep(t *testing.T) {
	r := newStepRig(t)
	s := r.step(t, "guest-image")
	if checkOK(t, s).OK {
		t.Fatal("passed with no image")
	}
	plan, err := s.Plan(context.Background())
	if err != nil || !plan.NeedsRoot {
		t.Errorf("plan = %+v, %v", plan, err)
	}
	if err := s.Apply(context.Background()); err != nil || r.builds != 1 {
		t.Fatalf("Apply = %v after %d builds", err, r.builds)
	}
	var paths []string
	for _, it := range r.records {
		paths = append(paths, it.Path)
	}
	for _, p := range []string{r.d.Paths.Kernel, r.d.Paths.RootDisk, r.d.Paths.ImagesSum, StampPath(r.d.Paths)} {
		if !slices.Contains(paths, p) {
			t.Errorf("%s not recorded: %v", p, paths)
		}
	}
	if !checkOK(t, s).OK {
		t.Error("a fresh image failed its check")
	}
}

func TestImageStepWantsARebuildWhenAnInputChanges(t *testing.T) {
	for name, change := range map[string]func(r *stepRig){
		"new CA":   func(r *stepRig) { os.WriteFile(r.d.CACert, []byte("another CA"), 0o644) },
		"new init": func(r *stepRig) { os.WriteFile(r.d.GuestInit, []byte("\x7fELF init v2"), 0o755) },
		"root disk": func(r *stepRig) {
			os.Remove(r.d.Paths.RootDisk) // 0444: replace it rather than write
			os.WriteFile(r.d.Paths.RootDisk, []byte("altered"), 0o444)
		},
		"kernel": func(r *stepRig) { os.WriteFile(r.d.Paths.Kernel, []byte("altered"), 0o644) },
		"kernel swapped with its sums": func(r *stepRig) {
			// Consistent sums alone don't do: the kernel must be the pinned one.
			other := []byte("\x7fELF another kernel")
			os.WriteFile(r.d.Paths.Kernel, other, 0o644)
			rootfs, _ := os.ReadFile(r.d.Paths.RootDisk)
			os.WriteFile(r.d.Paths.ImagesSum, []byte(fmt.Sprintf("%s  %s\n%s  %s\n",
				hexSum(other), r.d.Paths.Kernel, hexSum(rootfs), r.d.Paths.RootDisk)), 0o644)
		},
		"no stamp":      func(r *stepRig) { os.Remove(StampPath(r.d.Paths)) },
		"garbage stamp": func(r *stepRig) { os.WriteFile(StampPath(r.d.Paths), []byte("{"), 0o644) },
		"old pin": func(r *stepRig) {
			var s Stamp
			b, _ := os.ReadFile(StampPath(r.d.Paths))
			json.Unmarshal(b, &s)
			s.Claude = strings.Repeat("0", 64)
			b, _ = json.Marshal(s)
			os.WriteFile(StampPath(r.d.Paths), b, 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newStepRig(t)
			installImage(t, r.d.Paths, r.d.GuestInit, r.d.CACert, []byte("root disk"))
			s := r.step(t, "guest-image")
			if !checkOK(t, s).OK {
				t.Fatal("the image failed before the change")
			}
			change(r)
			if st := checkOK(t, s); st.OK {
				t.Errorf("still passed: %s", st.Detail)
			}
		})
	}
}

// A build that fails after creating files must still record them, or
// uninstall would never find them.
func TestImageStepRecordsWhatAFailedBuildLeft(t *testing.T) {
	r := newStepRig(t)
	r.d.Build = func(context.Context) (Stamp, error) {
		os.MkdirAll(r.d.Paths.StateDir, 0o755)
		os.WriteFile(r.d.Paths.RootDisk, []byte("half"), 0o444)
		return Stamp{}, errors.New("cc-launch clean: exit 1")
	}
	if err := r.step(t, "guest-image").Apply(context.Background()); err == nil {
		t.Fatal("Apply hid the build's error")
	}
	if len(r.records) != 1 || r.records[0].Path != r.d.Paths.RootDisk {
		t.Errorf("records = %+v, want the root disk the failed build left", r.records)
	}
}

func TestStepsUndoRemovesOnlyRecordedFiles(t *testing.T) {
	r := newStepRig(t)
	r.priv.On("rm").Return(system.Result{})
	items := []step.Item{{ID: "guest-image", Kind: step.KindFile, Path: r.d.Paths.RootDisk, State: step.Created}}
	if err := r.step(t, "guest-image").Undo(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	c := r.priv.Calls()
	if len(c) != 1 || !slices.Equal(c[0].Args, []string{"-f", "--", r.d.Paths.RootDisk}) || !c[0].Root {
		t.Errorf("undo ran %+v", c)
	}
}
