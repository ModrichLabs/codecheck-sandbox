package distro

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestFamiliesFromRealSamples(t *testing.T) {
	tests := []struct {
		file    string
		id      string
		version string
		family  Family
	}{
		{"arch", "arch", "", Arch},
		{"omarchy", "omarchy", "4.5.0", Arch},
		{"ubuntu-24.04", "ubuntu", "24.04", Debian},
		{"debian-12", "debian", "12", Debian},
		{"fedora-41", "fedora", "41", Fedora},
		{"rhel-9", "rhel", "9.4", Fedora},
		{"opensuse-tumbleweed", "opensuse-tumbleweed", "20241029", OpenSUSE},
		{"alpine", "alpine", "3.20.3", Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "os-release", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			rel, err := ParseOSRelease(data)
			if err != nil {
				t.Fatalf("ParseOSRelease: %v", err)
			}
			if rel.ID != tt.id || rel.VersionID != tt.version {
				t.Errorf("ID, VERSION_ID = %q, %q; want %q, %q", rel.ID, rel.VersionID, tt.id, tt.version)
			}
			if got := rel.Family(); got != tt.family {
				t.Errorf("Family = %q, want %q", got, tt.family)
			}
		})
	}
}

func TestParseOSReleaseQuotingRules(t *testing.T) {
	data := []byte(`# a comment
ID='single quoted'
ID_LIKE="rhel centos fedora"

PRETTY_NAME="Say \"hi\" \\ \$HOME \` + "`" + `x\` + "`" + `"
VERSION_ID=1.0
NOT A LINE
`)
	rel, err := ParseOSRelease(data)
	if err != nil {
		t.Fatalf("ParseOSRelease: %v", err)
	}
	if rel.ID != "single quoted" {
		t.Errorf("ID = %q", rel.ID)
	}
	if !reflect.DeepEqual(rel.IDLike, []string{"rhel", "centos", "fedora"}) {
		t.Errorf("IDLike = %q", rel.IDLike)
	}
	if want := "Say \"hi\" \\ $HOME `x`"; rel.PrettyName != want {
		t.Errorf("PrettyName = %q, want %q", rel.PrettyName, want)
	}
	if rel.Family() != Fedora {
		t.Errorf("Family = %q, want fedora through ID_LIKE", rel.Family())
	}
}

func TestParseOSReleaseDefaultsTheID(t *testing.T) {
	// os-release(5): a missing ID means "linux".
	rel, err := ParseOSRelease([]byte("NAME=Custom\nID_LIKE=debian\n"))
	if err != nil {
		t.Fatalf("ParseOSRelease: %v", err)
	}
	if rel.ID != "linux" || rel.Family() != Debian {
		t.Errorf("ID = %q, family = %q; want linux, debian through ID_LIKE", rel.ID, rel.Family())
	}
}

func TestDetectFallsBackToUsrLib(t *testing.T) {
	fsys := system.NewMemFS()
	_ = fsys.MkdirAll("/usr/lib", 0o755)
	_ = fsys.WriteFile("/usr/lib/os-release", []byte("ID=debian\nVERSION_ID=\"12\"\n"), 0o644)
	rel, err := Detect(fsys)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if rel.Family() != Debian {
		t.Errorf("Family = %q, want debian", rel.Family())
	}

	_ = fsys.MkdirAll("/etc", 0o755)
	_ = fsys.WriteFile("/etc/os-release", []byte("ID=fedora\n"), 0o644)
	if rel, _ := Detect(fsys); rel.Family() != Fedora {
		t.Errorf("Family = %q; /etc/os-release must win", rel.Family())
	}
}

func TestDetectWithNoFile(t *testing.T) {
	if _, err := Detect(system.NewMemFS()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want ErrNotExist", err)
	}
}
