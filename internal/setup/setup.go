// Package setup assembles the whole step list from spec §7 — platform
// checks, components, host network and guest image — from the parts each
// package provides, so setup, doctor and uninstall all walk the same steps.
package setup

import (
	"path/filepath"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/distro"
	"github.com/ModrichLabs/codecheck-sandbox/internal/fetch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/guest"
	"github.com/ModrichLabs/codecheck-sandbox/internal/hostnet"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/platform"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// Deps supplies what the steps need from the host.
type Deps struct {
	Platform platform.Env
	Family   distro.Family
	// Root runs root commands through the privilege tool (a PrivRunner);
	// commands without Root go to the user's own runner inside it.
	Root system.Runner
	// User names the account setup runs for; the sudoers rule lets it run
	// cc-launch.
	User string
	FS   system.FS
	// Look finds a tool on PATH or in the sbin folders (system.LookTool).
	Look func(string) (string, error)
	// APIKey supplies the key the wizard collected or CODECHECK_API_KEY;
	// it gets called only when the key step applies.
	APIKey func() (string, error)
	// GuestInit names the static cc-guestinit shipped beside the CLI.
	GuestInit string
	// Cache holds downloads and build files, ~/.cache/codecheck-sandbox/build.
	Cache string
	// CodeCheckCommit names the plugin commit to build; empty means the pin.
	CodeCheckCommit string
	// KernelFile supplies the guest kernel instead of the pinned URL
	// (setup --kernel).
	KernelFile string
	// Launcher runs cc-launch for the guest build (launch.Client).
	Launcher guest.Launcher
	// Downloader fetches the pinned files; its Progress reaches the screen.
	Downloader fetch.Downloader
	// Executable names the running binary, which the proxy step installs;
	// nil means os.Executable.
	Executable func() (string, error)
	// Record hears every item a step creates, for the manifest.
	Record func(step.Item)
	Now    func() time.Time
}

// Steps returns every setup step in order.
func Steps(d Deps) ([]step.Step, error) {
	steps := platform.Checks(d.Platform)
	steps = append(steps, distro.ComponentsStep(distro.ComponentsDeps{Family: d.Family, Root: d.Root, Look: d.Look, Record: d.Record}))

	cfg := hostnet.DefaultConfig()
	host, err := hostnet.Steps(hostnet.Deps{Config: cfg, Runner: d.Root, FS: d.FS, Record: d.Record, APIKey: d.APIKey, User: d.User,
		Executable: d.Executable})
	if err != nil {
		return nil, err
	}
	steps = append(steps, host...)

	paths := launch.DefaultPaths()
	sources := guest.DefaultSources(d.Downloader, system.ExecRunner{}, d.Cache, d.CodeCheckCommit)
	sources.KernelFile = d.KernelFile
	builder := &guest.Builder{
		Sources: sources, Run: system.ExecRunner{}, Root: d.Root, Launch: d.Launcher,
		Cache: d.Cache, GuestInit: d.GuestInit, CACert: cfg.CACertPath, Paths: paths, Now: d.Now,
		CodeCheckCommit: d.CodeCheckCommit,
	}
	release := fetch.FirecrackerRelease{Downloader: d.Downloader, Pin: pins.Firecracker, WorkDir: filepath.Join(d.Cache, "firecracker")}
	steps = append(steps, guest.Steps(guest.Deps{
		Paths: paths, Root: d.Root, Cache: d.Cache, GuestInit: d.GuestInit, CACert: cfg.CACertPath,
		Record: d.Record, FirecrackerPin: pins.Firecracker,
		FetchFirecracker: release.Fetch,
		Build:            builder.Build,
		CodeCheckCommit:  d.CodeCheckCommit,
	})...)
	return steps, nil
}

// Info lists the steps for the interface.
func Info(steps []step.Step) []tui.StepInfo {
	out := make([]tui.StepInfo, len(steps))
	for i, s := range steps {
		out[i] = tui.StepInfo{ID: s.ID(), Title: s.Title(), Phase: s.Phase()}
	}
	return out
}

// Sidebar lists the wizard's phases from spec §7.
func Sidebar() []tui.SidebarEntry {
	return []tui.SidebarEntry{
		{Title: "Welcome"},
		{Title: "Platform", Phase: step.PhasePlatform},
		{Title: "Components", Phase: step.PhaseComponents},
		{Title: "Your details"},
		{Title: "Host network", Phase: step.PhaseHost},
		{Title: "Guest build", Phase: step.PhaseGuest},
		{Title: "Verify", Phase: step.PhaseVerify},
		{Title: "Summary"},
	}
}
