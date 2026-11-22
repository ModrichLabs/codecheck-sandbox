package distro

import (
	"context"
	"fmt"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

const idComponents = "components"

func init() { telemetry.RegisterStepIDs(idComponents) }

// probes names the tools that show a package got installed, whatever the
// distro calls the package.
var probes = map[Package][]string{
	Nftables:      {"nft"},
	E2fsprogs:     {"mkfs.ext4", "debugfs"},
	SquashFSTools: {"unsquashfs"},
	Git:           {"git"},
	IPRoute2:      {"ip"},
	GnuPG:         {"gpg"},
}

// ComponentsDeps supplies what the components step needs.
type ComponentsDeps struct {
	Family Family
	Root   system.Runner // root commands, through the privilege tool
	// Look finds a tool on PATH or in the sbin folders (system.LookTool).
	Look   func(name string) (string, error)
	Record func(step.Item) // hears each package Apply installs; nil drops them
}

// ComponentsStep installs the packages setup needs (spec §7, step 3) that
// the host lacks, and records only those, so uninstall never removes a
// package setup found already installed.
func ComponentsStep(d ComponentsDeps) step.Step { return components{d} }

type components struct{ d ComponentsDeps }

func (components) ID() string        { return idComponents }
func (components) Title() string     { return "Install packages" }
func (components) Phase() step.Phase { return step.PhaseComponents }

func (c components) missing() []Package {
	var out []Package
	for _, p := range AllPackages {
		for _, tool := range probes[p] {
			if _, err := c.d.Look(tool); err != nil {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func (c components) Check(context.Context) (step.Status, error) {
	m := c.missing()
	if len(m) == 0 {
		return step.Status{OK: true, Detail: "every package in place"}, nil
	}
	names := make([]string, len(m))
	for i, p := range m {
		names[i] = string(p)
	}
	return step.Status{Detail: "missing: " + strings.Join(names, ", ")}, nil
}

func (c components) Plan(context.Context) (step.Plan, error) {
	cmds, err := InstallCommands(c.d.Family, c.missing())
	if err != nil {
		return step.Plan{}, err
	}
	p := step.Plan{NeedsRoot: len(cmds) > 0}
	for _, argv := range cmds {
		p.Cmds = append(p.Cmds, system.Cmd{Name: argv[0], Args: argv[1:], Root: true})
	}
	return p, nil
}

func (c components) Apply(ctx context.Context) error {
	m := c.missing()
	cmds, err := InstallCommands(c.d.Family, m)
	if err != nil {
		return err
	}
	for _, argv := range cmds {
		if err := run(ctx, c.d.Root, argv); err != nil {
			return err
		}
	}
	for _, p := range m {
		name, _ := PackageName(c.d.Family, p)
		if c.d.Record != nil {
			c.d.Record(step.Item{ID: idComponents, Kind: step.KindPackage, Name: name, State: step.Created})
		}
	}
	return nil
}

func (c components) Undo(ctx context.Context, items []step.Item) error {
	var names []string
	for _, it := range items {
		if err := step.CheckName(step.KindPackage, it.Name); err != nil {
			return err
		}
		names = append(names, it.Name)
	}
	argv, err := RemoveCommand(c.d.Family, names)
	if err != nil || argv == nil {
		return err
	}
	return run(ctx, c.d.Root, argv)
}

// RemoveCommand returns the root command that removes the named packages
// from family f without prompting; nil for no names.
func RemoveCommand(f Family, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	switch f {
	case Arch:
		return append([]string{"pacman", "-R", "--noconfirm"}, names...), nil
	case Debian:
		return append([]string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "remove", "-y"}, names...), nil
	case Fedora:
		return append([]string{"dnf", "remove", "-y"}, names...), nil
	case OpenSUSE:
		return append([]string{"zypper", "--non-interactive", "remove"}, names...), nil
	}
	return nil, fmt.Errorf("%s: %w", f, ErrUnknownFamily)
}

func run(ctx context.Context, r system.Runner, argv []string) error {
	res, err := r.Run(ctx, system.Cmd{Name: argv[0], Args: argv[1:], Root: true})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s: %s", strings.Join(argv[:min(len(argv), 3)], " "), strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
