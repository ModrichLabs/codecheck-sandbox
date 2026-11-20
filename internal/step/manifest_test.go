package step

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func sampleManifest() *Manifest {
	return &Manifest{
		Version:     ManifestVersion,
		InstalledBy: InstalledBy{Version: "0.1.0-dev", At: "2026-11-20T10:00:00Z"},
		Items: []Item{
			{ID: "jail-user", Kind: KindUser, Name: "fcjail", State: Created},
			{ID: "packages", Kind: KindPackage, Name: "nftables", State: Adopted},
			{ID: "rules", Kind: KindFile, Path: "/etc/codecheck-sandbox/rules.nft", State: Created},
			{ID: "sudoers", Kind: KindFile, Path: "/etc/sudoers.d/codecheck-sandbox", State: Created},
		},
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := sampleManifest()
	data, err := m.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("round trip =\n%+v\nwant\n%+v", got, m)
	}
	if !strings.Contains(string(data), `"installedBy"`) || !strings.Contains(string(data), `"state": "created"`) {
		t.Errorf("JSON lacks the schema's field names:\n%s", data)
	}
}

func TestManifestRejectsPathsOutsideTheAllowlist(t *testing.T) {
	bad := []string{
		"/etc/passwd",
		"/etc/sudoers.d/other",
		"/etc/codecheck-sandbox/../shadow",
		"/etc/codecheck-sandbox-evil/x",
		"/var/lib/codecheck-sandbox//x",
		"etc/codecheck-sandbox/x",
		"/",
	}
	for _, p := range bad {
		m := sampleManifest()
		m.Items[2].Path = p
		data, _ := json.Marshal(m) // bypasses Marshal's own check, like a tampered file
		if _, err := ParseManifest(data); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("path %q: err = %v, want ErrPathNotAllowed", p, err)
		}
		if _, err := m.Marshal(); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("path %q: Marshal err = %v, want ErrPathNotAllowed", p, err)
		}
	}
}

func TestManifestAllowsTheFootprint(t *testing.T) {
	good := []string{
		"/usr/local/lib/codecheck-sandbox/bin/cc-launch",
		"/etc/sudoers.d/codecheck-sandbox",
		"/var/lib/codecheck-sandbox",
		"/var/lib/codecheck-sandbox/rootfs.ext4",
		"/srv/jailer/firecracker",
		"/etc/codecheck-sandbox/api-key",
		"/etc/systemd/system/codecheck-net.service",
		"/etc/systemd/system/codecheck-proxy.service",
		"/etc/NetworkManager/conf.d/codecheck-sandbox.conf",
	}
	for _, p := range good {
		if err := CheckPath(p); err != nil {
			t.Errorf("CheckPath(%q) = %v, want nil", p, err)
		}
	}
}

func TestManifestRefusesUnknownVersion(t *testing.T) {
	_, err := ParseManifest([]byte(`{"version":2,"installedBy":{},"items":[]}`))
	if !errors.Is(err, ErrManifestVersion) {
		t.Errorf("err = %v, want ErrManifestVersion", err)
	}
}

func TestManifestRefusesUnknownState(t *testing.T) {
	m := sampleManifest()
	m.Items[0].State = "borrowed"
	if _, err := m.Marshal(); err == nil {
		t.Error("Marshal accepted state \"borrowed\"")
	}
	bad := `{"version":1,"installedBy":{},"items":[{"id":"x","kind":"user","name":"u","state":"borrowed"}]}`
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Error("ParseManifest accepted state \"borrowed\"")
	}
}

func TestManifestLoadAndSave(t *testing.T) {
	m := sampleManifest()
	want, _ := m.Marshal()

	f := &system.FakeRunner{}
	f.On("install").Return(system.Result{})
	f.On("dd").Return(system.Result{})
	f.On("mv").Return(system.Result{})
	if err := m.Save(context.Background(), f, ManifestPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Save fills a temporary file and renames it over the manifest, so a
	// failure part-way leaves the old manifest whole.
	tmp := ManifestPath + ".new"
	calls := f.Calls()
	if len(calls) != 3 || calls[1].Args[0] != "of="+tmp || string(calls[1].Stdin) != string(want) {
		t.Fatalf("Save calls = %+v", calls)
	}
	if calls[0].Args[1] != "0644" {
		t.Errorf("manifest mode = %s, want 0644", calls[0].Args[1])
	}
	wantMv := system.Cmd{Name: "mv", Args: []string{"-f", "-T", "--", tmp, ManifestPath}, Root: true}
	if !reflect.DeepEqual(calls[2], wantMv) {
		t.Errorf("rename = %+v, want %+v", calls[2], wantMv)
	}

	fsys := system.NewMemFS()
	_ = fsys.MkdirAll("/var/lib/codecheck-sandbox", 0o755)
	_ = fsys.WriteFile(ManifestPath, want, 0o644)
	got, err := LoadManifest(fsys, ManifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("LoadManifest = %+v, want %+v", got, m)
	}
}

func TestManifestSaveFailureSkipsTheRename(t *testing.T) {
	f := &system.FakeRunner{}
	f.On("install").Return(system.Result{})
	f.On("dd").Return(system.Result{Code: 1, Stderr: []byte("sudo: a password is required")})
	if err := sampleManifest().Save(context.Background(), f, ManifestPath); err == nil {
		t.Fatal("Save: want an error")
	}
	if n := len(f.Calls()); n != 2 {
		t.Errorf("calls = %d, want 2 (no rename after a failed write)", n)
	}
}

func TestManifestRefusesTrailingData(t *testing.T) {
	data, _ := sampleManifest().Marshal()
	for _, tail := range []string{`{"version":1}`, "garbage"} {
		if _, err := ParseManifest(append(append([]byte{}, data...), tail...)); err == nil {
			t.Errorf("ParseManifest accepted trailing %q", tail)
		}
	}
}

func TestManifestChecksNames(t *testing.T) {
	bad := []Item{
		{ID: "x", Kind: KindUser, Name: "root", State: Created},
		{ID: "x", Kind: KindPackage, Name: "openssh-server", State: Created},
		{ID: "x", Kind: KindUnit, Name: "sshd.service", State: Created},
		{ID: "x", Kind: KindNetwork, Name: "eth0", State: Created},
		{ID: "x", Kind: KindUser, Name: "", State: Created},
		{ID: "x", Kind: KindFile, Path: "/etc/codecheck-sandbox/a", Name: "fcjail", State: Created},
	}
	for _, it := range bad {
		m := &Manifest{Version: ManifestVersion, Items: []Item{it}}
		data, _ := json.Marshal(m)
		if _, err := ParseManifest(data); err == nil {
			t.Errorf("ParseManifest accepted %+v", it)
		}
	}
	good := []Item{
		{ID: "x", Kind: KindUser, Name: "fcjail", State: Created},
		{ID: "x", Kind: KindPackage, Name: "squashfs-tools", State: Created},
		{ID: "x", Kind: KindPackage, Name: "iproute", State: Adopted}, // Fedora's name
		{ID: "x", Kind: KindUnit, Name: "codecheck-proxy.service", State: Created},
		{ID: "x", Kind: KindNetwork, Name: "cc-tap0", State: Created},
		{ID: "x", Kind: KindNetwork, Name: "inet codecheck", State: Created},
		{ID: "x", Kind: KindNetwork, Name: "firewalld zone codecheck", State: Created},
		{ID: "x", Kind: KindNetwork, Name: "ufw allow cc-tap0 3180", State: Created},
	}
	for _, it := range good {
		m := &Manifest{Version: ManifestVersion, Items: []Item{it}}
		if _, err := m.Marshal(); err != nil {
			t.Errorf("Marshal(%+v) = %v, want nil", it, err)
		}
	}
}

func TestLoadManifestMissing(t *testing.T) {
	_, err := LoadManifest(system.NewMemFS(), ManifestPath)
	if !errors.Is(err, ErrNoManifest) {
		t.Errorf("err = %v, want ErrNoManifest", err)
	}
}
