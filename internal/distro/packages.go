package distro

import (
	"errors"
	"fmt"
	"strings"
)

// Package names a logical package from spec §3, whatever the distro calls it.
type Package string

const (
	Nftables      Package = "nftables"
	E2fsprogs     Package = "e2fsprogs"
	SquashFSTools Package = "squashfs-tools"
	Git           Package = "git"
	IPRoute2      Package = "iproute2"
	GnuPG         Package = "gnupg"
)

// AllPackages lists what setup installs, in install order.
var AllPackages = []Package{Nftables, E2fsprogs, SquashFSTools, Git, IPRoute2, GnuPG}

// ErrUnknownFamily reports a distro whose package manager setup does not know.
// The wizard shows the package list and pauses for a manual install.
var ErrUnknownFamily = errors.New("unknown distribution family")

// renamed holds the names that differ from the logical one.
var renamed = map[Family]map[Package]string{
	Fedora:   {IPRoute2: "iproute", GnuPG: "gnupg2"},
	OpenSUSE: {SquashFSTools: "squashfs", GnuPG: "gpg2"},
}

// PackageName returns p's package name on family f.
func PackageName(f Family, p Package) (string, error) {
	switch f {
	case Arch, Debian, Fedora, OpenSUSE:
	default:
		return "", fmt.Errorf("%s: %w", f, ErrUnknownFamily)
	}
	if name, ok := renamed[f][p]; ok {
		return name, nil
	}
	return string(p), nil
}

// InstallCommands returns the root commands, in order, that install pkgs on
// family f without prompting. Debian refreshes its package lists first, and
// sets DEBIAN_FRONTEND through env, since root commands carry no Env.
func InstallCommands(f Family, pkgs []Package) ([][]string, error) {
	names := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		name, err := PackageName(f, p)
		if err != nil {
			list := make([]string, len(pkgs))
			for i, p := range pkgs {
				list[i] = string(p)
			}
			return nil, fmt.Errorf("%w: install these packages by hand: %s", err, strings.Join(list, ", "))
		}
		names = append(names, name)
	}
	switch f {
	case Arch:
		return [][]string{append([]string{"pacman", "-S", "--needed", "--noconfirm"}, names...)}, nil
	case Debian:
		return [][]string{
			{"apt-get", "update"},
			append([]string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends"}, names...),
		}, nil
	case Fedora:
		return [][]string{append([]string{"dnf", "install", "-y"}, names...)}, nil
	default: // OpenSUSE; PackageName already refused anything else
		return [][]string{append([]string{"zypper", "--non-interactive", "install", "--no-recommends"}, names...)}, nil
	}
}
