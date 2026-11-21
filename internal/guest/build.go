package guest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/guestinit"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Sources fetches the pinned inputs; each writes dest only after its check.
type Sources interface {
	Kernel(ctx context.Context, dest string) error
	Base(ctx context.Context, dest string) error
	Claude(ctx context.Context, dest string) error
	Plugin(ctx context.Context) ([]byte, error)
}

// Launcher runs cc-launch (launch.Client).
type Launcher interface {
	Build(ctx context.Context, id launch.RunID) error
	Clean(ctx context.Context, id launch.RunID) error
}

// Builder makes the guest root disk. Nothing in it runs with network inside
// the VM: the host fetches and checks everything first.
type Builder struct {
	Sources   Sources
	Run       system.Runner // the user's own commands (mkfs.ext4)
	Root      system.Runner // root commands, through the privilege tool
	Launch    Launcher
	Cache     string // the user's build cache, ~/.cache/codecheck-sandbox/build
	GuestInit string // the static cc-guestinit, shipped beside the CLI
	CACert    string // the sandbox CA certificate
	Paths     launch.Paths
	Now       func() time.Time
}

// StampVersion changes whenever the stamp's meaning does.
const StampVersion = 1

// Stamp records what a guest image got built from, so a later check can tell
// whether any input changed since: a new pin, CA, init or guest script.
type Stamp struct {
	Version   int    `json:"version"`
	Kernel    string `json:"kernel_sha256"`
	Base      string `json:"base_sha256"`
	Claude    string `json:"claude_sha256"`
	CodeCheck string `json:"codecheck_commit"`
	CA        string `json:"ca_sha256"`
	GuestInit string `json:"guestinit_sha256"`
	Scripts   string `json:"scripts_sha256"`
	RootDisk  string `json:"rootdisk_sha256"` // the result; empty in WantStamp
}

// StampPath names the stamp beside the images.
func StampPath(p launch.Paths) string { return filepath.Join(p.StateDir, "build.json") }

// buildPins holds the pins a build answers to; tests swap in their own
// stand-in kernel's hash.
var buildPins = struct{ Kernel, Base, Claude, CodeCheck string }{
	pins.Kernel.SHA256, pins.Ubuntu.SquashFSSHA256, pins.ClaudeCode.BinarySHA256, pins.CodeCheck.Commit,
}

// WantStamp describes the image the current pins and files would build.
func WantStamp(guestInit, caCert string) (Stamp, error) {
	initSum, err := fileSHA256(guestInit)
	if err != nil {
		return Stamp{}, err
	}
	caSum, err := fileSHA256(caCert)
	if err != nil {
		return Stamp{}, err
	}
	h := sha256.New()
	for _, s := range []string{"cc-claude", "cc-selftest"} {
		b, err := assets.Guest.ReadFile("guest/" + s)
		if err != nil {
			return Stamp{}, err
		}
		fmt.Fprintf(h, "%s %d\n", s, len(b))
		h.Write(b)
	}
	return Stamp{
		Version: StampVersion, Kernel: buildPins.Kernel, Base: buildPins.Base,
		Claude: buildPins.Claude, CodeCheck: buildPins.CodeCheck,
		CA: caSum, GuestInit: initSum, Scripts: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// runFiles lists what the run folder gets, in order; cc-launch's checks want
// exactly these.
var runFiles = []string{"config.img", "vmlinux", "init.cpio", "base.squashfs", "inputs.ext4"}

// Build fetches and checks every input, packs the builder's disks, boots the
// builder through cc-launch, then installs the result with images.sha256 and
// the stamp. A run folder that got created gets cleaned on every path out.
func (b *Builder) Build(ctx context.Context) (Stamp, error) {
	stamp, err := WantStamp(b.GuestInit, b.CACert)
	if err != nil {
		return Stamp{}, err
	}
	if err := os.MkdirAll(b.Cache, 0o700); err != nil {
		return Stamp{}, err
	}
	cache := func(name string) string { return filepath.Join(b.Cache, name) }

	for _, f := range []struct {
		name  string
		fetch func(context.Context, string) error
	}{
		{"vmlinux", b.Sources.Kernel}, {"base.squashfs", b.Sources.Base}, {"claude", b.Sources.Claude},
	} {
		if err := f.fetch(ctx, cache(f.name)); err != nil {
			return Stamp{}, err
		}
	}
	plugin, err := b.Sources.Plugin(ctx)
	if err != nil {
		return Stamp{}, err
	}
	ca, err := os.ReadFile(b.CACert)
	if err != nil {
		return Stamp{}, fmt.Errorf("reading the sandbox CA: %w", err)
	}
	in := Inputs{Claude: cache("claude"), GuestInit: b.GuestInit, CACert: ca, Plugin: plugin}
	if err := MakeInputs(ctx, b.Run, b.Cache, cache("inputs.ext4"), in); err != nil {
		return Stamp{}, err
	}
	init, err := os.ReadFile(b.GuestInit)
	if err != nil {
		return Stamp{}, err
	}
	if err := os.WriteFile(cache("init.cpio"), Initramfs(init), 0o644); err != nil {
		return Stamp{}, err
	}
	id, err := launch.NewRunID(b.Now())
	if err != nil {
		return Stamp{}, err
	}
	block, err := guestinit.Encode(guestinit.Config{Mode: guestinit.ModeBuild, RunID: id.String()})
	if err != nil {
		return Stamp{}, err
	}
	if err := os.WriteFile(cache("config.img"), block, 0o644); err != nil {
		return Stamp{}, err
	}

	// From here on, root-owned files exist: clean the run on every path out.
	run := id.Dir(b.Paths)
	done := false
	defer func() {
		if !done {
			_ = b.Launch.Clean(context.WithoutCancel(ctx), id)
		}
	}()
	for _, dir := range []string{b.Paths.RunsDir, run} {
		if err := b.root(ctx, "install", "-d", "-m", "0755", "-o", "root", "-g", "root", dir); err != nil {
			return Stamp{}, err
		}
	}
	for _, f := range runFiles {
		if err := b.root(ctx, "install", "-m", "0644", "-o", "root", "-g", "root", "-T", cache(f), filepath.Join(run, f)); err != nil {
			return Stamp{}, err
		}
	}
	if err := b.Launch.Build(ctx, id); err != nil {
		return Stamp{}, err
	}

	if err := b.install(ctx, filepath.Join(run, "rootfs.ext4"), cache("vmlinux"), &stamp); err != nil {
		return Stamp{}, err
	}
	done = true
	return stamp, b.Launch.Clean(ctx, id)
}

// install puts the built disk and the kernel in place with their sums and
// the stamp. Everything lands beside the old image as *.new first, gets
// checked as root-owned files, and only then renames over the old files, so
// a failure on the way leaves the old image whole and consistent.
func (b *Builder) install(ctx context.Context, built, kernel string, stamp *Stamp) (err error) {
	final := []string{b.Paths.RootDisk, b.Paths.Kernel, b.Paths.ImagesSum, StampPath(b.Paths)}
	staged := map[string]bool{}
	defer func() {
		if err != nil {
			for _, f := range final {
				if staged[f] {
					_ = b.root(context.WithoutCancel(ctx), "rm", "-f", "--", f+".new")
				}
			}
		}
	}()

	// The run folder belongs to root, so its disk can't change under us.
	if stamp.RootDisk, err = fileSHA256(built); err != nil {
		return err
	}
	staged[b.Paths.RootDisk] = true
	if err := b.root(ctx, "mv", "-f", "-T", built, b.Paths.RootDisk+".new"); err != nil {
		return err
	}
	staged[b.Paths.Kernel] = true
	if err := b.root(ctx, "install", "-m", "0644", "-o", "root", "-g", "root", "-T", kernel, b.Paths.Kernel+".new"); err != nil {
		return err
	}
	// Hash the root-owned copy, not the user's cache, which could have
	// changed since the fetch checked it.
	kernelSum, err := fileSHA256(b.Paths.Kernel + ".new")
	if err != nil {
		return err
	}
	if kernelSum != stamp.Kernel {
		return fmt.Errorf("the kernel to install hashes to %s, not its pin %s", kernelSum, stamp.Kernel)
	}

	sums := fmt.Sprintf("%s  %s\n%s  %s\n", kernelSum, b.Paths.Kernel, stamp.RootDisk, b.Paths.RootDisk)
	staged[b.Paths.ImagesSum] = true
	if err := system.WriteRootFile(ctx, b.Root, b.Paths.ImagesSum+".new", []byte(sums), 0o644); err != nil {
		return err
	}
	js, err := json.MarshalIndent(stamp, "", "  ")
	if err != nil {
		return err
	}
	staged[StampPath(b.Paths)] = true
	if err := system.WriteRootFile(ctx, b.Root, StampPath(b.Paths)+".new", append(js, '\n'), 0o644); err != nil {
		return err
	}
	for _, f := range final {
		if err := b.root(ctx, "mv", "-f", "-T", f+".new", f); err != nil {
			return err
		}
		staged[f] = false
	}
	return nil
}

func (b *Builder) root(ctx context.Context, name string, args ...string) error {
	res, err := b.Root.Run(ctx, system.Cmd{Name: name, Args: args, Root: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s %s: exit %d: %s", name, strings.Join(args, " "), res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
