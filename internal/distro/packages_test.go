package distro

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestInstallCommandsGolden(t *testing.T) {
	for _, f := range []Family{Arch, Debian, Fedora, OpenSUSE} {
		t.Run(string(f), func(t *testing.T) {
			cmds, err := InstallCommands(f, AllPackages)
			if err != nil {
				t.Fatalf("InstallCommands: %v", err)
			}
			var got strings.Builder
			for _, argv := range cmds {
				got.WriteString(strings.Join(argv, " ") + "\n")
			}
			golden := filepath.Join("testdata", "golden", "install-"+string(f)+".txt")
			if *update {
				if err := os.WriteFile(golden, []byte(got.String()), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading %s: %v", golden, err)
			}
			if got.String() != string(want) {
				t.Errorf("install commands for %s =\n%s\nwant\n%s", f, got.String(), want)
			}
		})
	}
}

func TestUnknownFamilyListsThePackages(t *testing.T) {
	_, err := InstallCommands(Unknown, AllPackages)
	if !errors.Is(err, ErrUnknownFamily) {
		t.Fatalf("err = %v, want ErrUnknownFamily", err)
	}
	for _, p := range AllPackages {
		if !strings.Contains(err.Error(), string(p)) {
			t.Errorf("error %q omits %s, which the user must install by hand", err, p)
		}
	}
}

// The manifest refuses package names outside its allowlist, so every name
// setup can install must appear there.
func TestEveryPackageNamePassesTheManifestAllowlist(t *testing.T) {
	for _, f := range []Family{Arch, Debian, Fedora, OpenSUSE} {
		for _, p := range AllPackages {
			name, err := PackageName(f, p)
			if err != nil {
				t.Fatalf("PackageName(%s, %s): %v", f, p, err)
			}
			if err := step.CheckName(step.KindPackage, name); err != nil {
				t.Errorf("%s on %s installs %q, which the manifest would refuse: %v", p, f, name, err)
			}
		}
	}
}

func TestPackageNamesPerFamily(t *testing.T) {
	tests := []struct {
		f    Family
		p    Package
		want string
	}{
		{Fedora, IPRoute2, "iproute"},
		{Fedora, GnuPG, "gnupg2"},
		{OpenSUSE, SquashFSTools, "squashfs"},
		{OpenSUSE, GnuPG, "gpg2"},
		{Debian, GnuPG, "gnupg"},
		{Arch, Nftables, "nftables"},
	}
	for _, tt := range tests {
		if got, _ := PackageName(tt.f, tt.p); got != tt.want {
			t.Errorf("PackageName(%s, %s) = %q, want %q", tt.f, tt.p, got, tt.want)
		}
	}
}
