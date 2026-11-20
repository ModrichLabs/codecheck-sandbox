package step

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// ManifestPath holds the root-owned record of what setup installed.
const ManifestPath = "/var/lib/codecheck-sandbox/manifest.json"

// ManifestVersion names the only schema this release reads and writes.
const ManifestVersion = 1

var (
	ErrNoManifest      = errors.New("no manifest")
	ErrManifestVersion = errors.New("unsupported manifest version")
	ErrPathNotAllowed  = errors.New("path outside the codecheck-sandbox footprint")
	ErrNameNotAllowed  = errors.New("name outside the codecheck-sandbox footprint")
)

// ItemState records whether setup made an item or found it already there.
// Uninstall removes only created items.
type ItemState string

const (
	Created ItemState = "created"
	Adopted ItemState = "adopted"
)

// ItemKind says what sort of thing an item names.
type ItemKind string

const (
	KindFile    ItemKind = "file"
	KindDir     ItemKind = "dir"
	KindUser    ItemKind = "user"
	KindPackage ItemKind = "package"
	KindUnit    ItemKind = "unit"
	KindNetwork ItemKind = "network" // a tap device or an nftables table
)

// Manifest lists everything setup put on the machine (spec §8). Uninstall
// treats it as hints: every path still has to pass the allowlist.
type Manifest struct {
	Version     int         `json:"version"`
	InstalledBy InstalledBy `json:"installedBy"`
	Items       []Item      `json:"items"`
}

// InstalledBy records which release wrote the manifest, so later releases can
// migrate it.
type InstalledBy struct {
	Version string `json:"version"`
	At      string `json:"at"` // RFC 3339
}

// Item names one installed thing. ID matches the Step that owns it. Files and
// folders carry a Path; users, packages, units and network items a Name.
type Item struct {
	ID    string    `json:"id"`
	Kind  ItemKind  `json:"kind"`
	Path  string    `json:"path,omitempty"`
	Name  string    `json:"name,omitempty"`
	State ItemState `json:"state"`
}

// allowedFiles and allowedDirs hold the footprint from spec §3. A folder entry
// allows itself and everything below it.
var (
	allowedFiles = []string{
		"/etc/sudoers.d/codecheck-sandbox",
		"/etc/systemd/system/codecheck-net.service",
		"/etc/systemd/system/codecheck-proxy.service",
		"/etc/NetworkManager/conf.d/codecheck-sandbox.conf",
	}
	allowedDirs = []string{
		"/usr/local/lib/codecheck-sandbox",
		"/var/lib/codecheck-sandbox",
		"/etc/codecheck-sandbox",
		"/srv/jailer",
	}
)

// allowedNames holds every name a non-file item may carry: the jail user, the
// systemd units, the tap device and nftables table, and the distro packages
// from spec §3 under each family's name. The components step (M6) must keep
// the package list in step with what it installs.
var allowedNames = map[ItemKind][]string{
	KindUser:    {"fcjail"},
	KindUnit:    {"codecheck-net.service", "codecheck-proxy.service"},
	KindNetwork: {"cc-tap0", "inet codecheck"},
	KindPackage: {
		"nftables", "e2fsprogs", "git",
		"squashfs-tools", "squashfs", // openSUSE: squashfs
		"iproute2", "iproute", // Fedora/RHEL: iproute
		"gnupg", "gnupg2", "gpg2", // Fedora/RHEL: gnupg2; openSUSE: gpg2
	},
}

// CheckName accepts only the names in allowedNames for kind, so a tampered
// manifest cannot steer uninstall at other users, packages or services.
func CheckName(kind ItemKind, name string) error {
	if slices.Contains(allowedNames[kind], name) {
		return nil
	}
	return fmt.Errorf("%s %q: %w", kind, name, ErrNameNotAllowed)
}

// CheckPath accepts only absolute, already-clean paths inside the footprint,
// so a tampered manifest cannot steer uninstall at other files.
func CheckPath(p string) error {
	if filepath.IsAbs(p) && filepath.Clean(p) == p {
		for _, f := range allowedFiles {
			if p == f {
				return nil
			}
		}
		for _, d := range allowedDirs {
			if p == d || strings.HasPrefix(p, d+"/") {
				return nil
			}
		}
	}
	return fmt.Errorf("%q: %w", p, ErrPathNotAllowed)
}

func (m *Manifest) validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("%w %d (this release reads %d)", ErrManifestVersion, m.Version, ManifestVersion)
	}
	for _, it := range m.Items {
		switch it.State {
		case Created, Adopted:
		default:
			return fmt.Errorf("item %s: unknown state %q", it.ID, it.State)
		}
		switch it.Kind {
		case KindFile, KindDir:
			if it.Name != "" {
				return fmt.Errorf("item %s: a %s item carries no name", it.ID, it.Kind)
			}
			if err := CheckPath(it.Path); err != nil {
				return fmt.Errorf("item %s: %w", it.ID, err)
			}
		case KindUser, KindPackage, KindUnit, KindNetwork:
			if it.Path != "" {
				return fmt.Errorf("item %s: a %s item carries no path", it.ID, it.Kind)
			}
			if err := CheckName(it.Kind, it.Name); err != nil {
				return fmt.Errorf("item %s: %w", it.ID, err)
			}
		default:
			return fmt.Errorf("item %s: unknown kind %q", it.ID, it.Kind)
		}
	}
	return nil
}

// Marshal validates m and encodes it as indented JSON.
func (m *Manifest) Marshal() ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseManifest decodes and validates a manifest, refusing unknown fields and
// versions.
func ParseManifest(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("reading the manifest: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("reading the manifest: unexpected data after the JSON object")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// LoadManifest reads the manifest at path. A missing file gives ErrNoManifest,
// which sends uninstall to its footprint scan.
func LoadManifest(fsys system.FS, path string) (*Manifest, error) {
	data, err := fsys.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, ErrNoManifest)
	}
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}

// Save writes the manifest as a root-owned, world-readable file. It fills
// path+".new" first and renames that over path, so a failure part-way (an
// expired login, Ctrl-C) leaves the previous manifest whole. r must handle
// Root commands.
func (m *Manifest) Save(ctx context.Context, r system.Runner, path string) error {
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := system.WriteRootFile(ctx, r, tmp, data, 0o644); err != nil {
		return err
	}
	res, err := r.Run(ctx, system.Cmd{Name: "mv", Args: []string{"-f", "-T", "--", tmp, path}, Root: true})
	if err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	if res.Code != 0 {
		return fmt.Errorf("replacing %s: mv exited with status %d: %s", path, res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}
