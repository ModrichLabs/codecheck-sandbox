package step

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
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
			if err := CheckPath(it.Path); err != nil {
				return fmt.Errorf("item %s: %w", it.ID, err)
			}
		case KindUser, KindPackage, KindUnit, KindNetwork:
			if it.Path != "" {
				return fmt.Errorf("item %s: a %s item carries no path", it.ID, it.Kind)
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

// Save writes the manifest as a root-owned, world-readable file. r must
// handle Root commands.
func (m *Manifest) Save(ctx context.Context, r system.Runner, path string) error {
	data, err := m.Marshal()
	if err != nil {
		return err
	}
	return system.WriteRootFile(ctx, r, path, data, 0o644)
}
