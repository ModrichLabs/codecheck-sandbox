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

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

const (
	idFirecracker = "guest-firecracker"
	idImage       = "guest-image"
)

func init() { telemetry.RegisterStepIDs(idFirecracker, idImage) }

// Deps supplies what the guest steps need.
type Deps struct {
	Paths     launch.Paths
	Root      system.Runner // root commands, through the privilege tool
	Cache     string
	GuestInit string
	CACert    string
	// CodeCheckCommit names the chosen plugin commit; empty means the pin.
	CodeCheckCommit string
	Record          func(step.Item) // hears every item Apply creates; nil drops them

	FirecrackerPin pins.FirecrackerPin
	// FetchFirecracker writes both checked binaries (fetch.FirecrackerRelease).
	FetchFirecracker func(ctx context.Context, firecrackerDest, jailerDest string) error
	// Build makes and installs the guest image (Builder.Build).
	Build func(ctx context.Context) (Stamp, error)
}

// Steps returns the guest phase: the Firecracker binaries, then the image.
func Steps(d Deps) []step.Step {
	return []step.Step{d.firecracker(), d.image()}
}

// guestStep holds one step's functions.
type guestStep struct {
	id, title string
	check     func(context.Context) (step.Status, error)
	plan      func(context.Context) (step.Plan, error)
	apply     func(context.Context) error
	undo      func(context.Context, []step.Item) error
}

func (s guestStep) ID() string                                     { return s.id }
func (s guestStep) Title() string                                  { return s.title }
func (s guestStep) Phase() step.Phase                              { return step.PhaseGuest }
func (s guestStep) Check(ctx context.Context) (step.Status, error) { return s.check(ctx) }
func (s guestStep) Plan(ctx context.Context) (step.Plan, error)    { return s.plan(ctx) }
func (s guestStep) Apply(ctx context.Context) error                { return s.apply(ctx) }

func (s guestStep) Undo(ctx context.Context, items []step.Item) error { return s.undo(ctx, items) }

func pass(detail string) (step.Status, error) { return step.Status{OK: true, Detail: detail}, nil }
func fail(detail string) (step.Status, error) { return step.Status{Detail: detail}, nil }

func (d Deps) record(it step.Item) {
	if d.Record != nil {
		d.Record(it)
	}
}

func (d Deps) root(ctx context.Context, name string, args ...string) error {
	res, err := d.Root.Run(ctx, system.Cmd{Name: name, Args: args, Root: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s %s: exit %d: %s", name, strings.Join(args, " "), res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// undo removes file items, then folder items that ended up empty.
func (d Deps) undo(ctx context.Context, items []step.Item) error {
	var errs []error
	var dirs []string
	for _, it := range items {
		switch it.Kind {
		case step.KindFile:
			errs = append(errs, d.root(ctx, "rm", "-f", "--", it.Path))
		case step.KindDir:
			dirs = append(dirs, it.Path)
		}
	}
	slices.SortStableFunc(dirs, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, p := range dirs {
		errs = append(errs, d.root(ctx, "rmdir", "--ignore-fail-on-non-empty", "--", p))
	}
	return errors.Join(errs...)
}

// --- Firecracker ---

func (d Deps) firecracker() step.Step {
	bins := []struct{ name, dest, sha string }{
		{"firecracker", d.Paths.Firecracker, d.FirecrackerPin.FirecrackerSHA256},
		{"jailer", d.Paths.Jailer, d.FirecrackerPin.JailerSHA256},
	}
	install := func(name, dest string) system.Cmd {
		return system.Cmd{Name: "install", Args: []string{"-D", "-m", "0755", "-o", "root", "-g", "root", "-T", filepath.Join(d.Cache, name), dest}, Root: true}
	}
	return guestStep{
		id: idFirecracker, title: "Firecracker " + d.FirecrackerPin.Version + " and its jailer",
		check: func(context.Context) (step.Status, error) {
			for _, b := range bins {
				if got, err := fileSHA256(b.dest); err != nil || got != b.sha {
					return fail(b.dest + " missing or not the pinned " + d.FirecrackerPin.Version)
				}
			}
			return pass("Firecracker " + d.FirecrackerPin.Version + " in place")
		},
		plan: func(context.Context) (step.Plan, error) {
			var cmds []step.PlannedCmd
			for _, b := range bins {
				cmds = append(cmds, install(b.name, b.dest))
			}
			return step.Plan{Cmds: cmds, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			if err := os.MkdirAll(d.Cache, 0o700); err != nil {
				return err
			}
			if err := d.FetchFirecracker(ctx, filepath.Join(d.Cache, "firecracker"), filepath.Join(d.Cache, "jailer")); err != nil {
				return err
			}
			dirExisted := exists(d.Paths.BinDir)
			for _, b := range bins {
				existed := exists(b.dest)
				c := install(b.name, b.dest)
				if err := d.root(ctx, c.Name, c.Args...); err != nil {
					return err
				}
				if !existed {
					d.record(step.Item{ID: idFirecracker, Kind: step.KindFile, Path: b.dest, State: step.Created})
				}
			}
			if !dirExisted {
				d.record(step.Item{ID: idFirecracker, Kind: step.KindDir, Path: d.Paths.BinDir, State: step.Created})
			}
			return nil
		},
		undo: d.undo,
	}
}

// --- Image ---

func (d Deps) image() step.Step {
	files := []string{d.Paths.Kernel, d.Paths.RootDisk, d.Paths.ImagesSum, StampPath(d.Paths)}
	return guestStep{
		id: idImage, title: "Guest image",
		check: func(context.Context) (step.Status, error) { return d.checkImage() },
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{
				Cmds: []step.PlannedCmd{{Name: filepath.Join(d.Paths.BinDir, "cc-launch"), Args: []string{"build", "<new run id>"}, Root: true}},
				Files: []step.PlannedFile{
					{Path: d.Paths.ImagesSum, Mode: 0o644, Content: []byte("(the SHA-256 of the kernel and the built root disk)\n")},
					{Path: StampPath(d.Paths), Mode: 0o644, Content: []byte("(a record of every input the build used)\n")},
				},
				NeedsRoot: true,
			}, nil
		},
		apply: func(ctx context.Context) error {
			existed := map[string]bool{}
			for _, f := range files {
				existed[f] = exists(f)
			}
			_, err := d.Build(ctx)
			// Record what exists now even after a failure, so uninstall
			// finds whatever a half-finished build left.
			for _, f := range files {
				if !existed[f] && exists(f) {
					d.record(step.Item{ID: idImage, Kind: step.KindFile, Path: f, State: step.Created})
				}
			}
			return err
		},
		undo: d.undo,
	}
}

// checkImage passes only when the stamp matches what the current inputs would
// build and the installed files match both images.sha256 and the stamp.
func (d Deps) checkImage() (step.Status, error) {
	b, err := os.ReadFile(StampPath(d.Paths))
	if err != nil {
		return fail("no guest image built yet")
	}
	var got Stamp
	if err := json.Unmarshal(b, &got); err != nil {
		return fail(StampPath(d.Paths) + " unreadable; rebuild")
	}
	want, err := WantStamp(d.GuestInit, d.CACert, d.CodeCheckCommit)
	if err != nil {
		return step.Status{}, err
	}
	want.RootDisk = got.RootDisk
	if got != want {
		return fail("the image's inputs changed since it got built (a pin, the CA, cc-guestinit or a guest script)")
	}
	sums, err := os.ReadFile(d.Paths.ImagesSum)
	if err != nil {
		return fail("no " + d.Paths.ImagesSum)
	}
	kernel, err1 := fileSHA256(d.Paths.Kernel)
	rootDisk, err2 := fileSHA256(d.Paths.RootDisk)
	if err1 != nil || err2 != nil {
		return fail("the kernel or root disk is missing")
	}
	wantSums := fmt.Sprintf("%s  %s\n%s  %s\n", kernel, d.Paths.Kernel, rootDisk, d.Paths.RootDisk)
	if string(sums) != wantSums || rootDisk != got.RootDisk || kernel != got.Kernel {
		return fail("the installed images differ from their recorded hashes")
	}
	return pass("guest image current")
}
