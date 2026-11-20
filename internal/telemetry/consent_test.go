package telemetry

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func ptr(b bool) *bool { return &b }

func TestResolveEveryCombination(t *testing.T) {
	configs := []struct {
		name string
		v    *bool
	}{{"unset", nil}, {"true", ptr(true)}, {"false", ptr(false)}}
	for _, dnt := range []string{"", "1", "0", "true"} {
		for _, noTel := range []bool{false, true} {
			for _, yes := range []bool{false, true} {
				for _, tty := range []bool{false, true} {
					for _, cfg := range configs {
						in := ConsentInputs{DoNotTrack: dnt, NoTelemetry: noTel, AssumeYes: yes, TTY: tty, Config: cfg.v}
						// Spec §11, as a table: DO_NOT_TRACK or --no-telemetry
						// always win; an explicit config decides next; with
						// nothing stored, only an interactive run may ask.
						want := Off
						switch {
						case (dnt != "" && dnt != "0") || noTel:
						case cfg.v != nil && *cfg.v:
							want = On
						case cfg.v != nil:
						case tty && !yes:
							want = Ask
						}
						name := fmt.Sprintf("dnt=%q/no-telemetry=%v/yes=%v/tty=%v/config=%s", dnt, noTel, yes, tty, cfg.name)
						if got := Resolve(in); got != want {
							t.Errorf("%s: Resolve = %v, want %v", name, got, want)
						}
					}
				}
			}
		}
	}
}

func TestResolveNamedCases(t *testing.T) {
	if Resolve(ConsentInputs{AssumeYes: true, TTY: true, Config: ptr(true)}) != On {
		t.Error("--yes with telemetry = true must stay on")
	}
	if Resolve(ConsentInputs{AssumeYes: true, TTY: true}) != Off {
		t.Error("--yes without an explicit telemetry = true must stay off")
	}
	if Resolve(ConsentInputs{DoNotTrack: "1", Config: ptr(true)}) != Off {
		t.Error("DO_NOT_TRACK=1 must beat telemetry = true")
	}
	if Resolve(ConsentInputs{}) != Off {
		t.Error("no TTY and no config must stay off")
	}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestInstallIDCreatedOnceAndPrivate(t *testing.T) {
	fsys := system.NewMemFS()
	dir := "/home/u/.local/state/codecheck-sandbox/telemetry"
	id, err := EnsureInstallID(fsys, dir)
	if err != nil {
		t.Fatalf("EnsureInstallID: %v", err)
	}
	if !uuidV4.MatchString(id) {
		t.Errorf("id %q is not a UUIDv4", id)
	}
	info, err := fsys.Stat(filepath.Join(dir, InstallIDFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("install id file: %v, %v; want mode 0600", info, err)
	}
	again, err := EnsureInstallID(fsys, dir)
	if err != nil || again != id {
		t.Errorf("second EnsureInstallID = %q, %v; want the same id", again, err)
	}
}

func TestInstallIDsDiffer(t *testing.T) {
	a, _ := EnsureInstallID(system.NewMemFS(), "/a")
	b, _ := EnsureInstallID(system.NewMemFS(), "/b")
	if a == b {
		t.Errorf("two installs got the same id %q", a)
	}
}

func TestInstallIDRejectsATamperedFile(t *testing.T) {
	fsys := system.NewMemFS()
	_ = fsys.MkdirAll("/t", 0o700)
	_ = fsys.WriteFile("/t/"+InstallIDFile, []byte("alice@laptop\n"), 0o600)
	id, err := EnsureInstallID(fsys, "/t")
	if err != nil {
		t.Fatalf("EnsureInstallID: %v", err)
	}
	if !uuidV4.MatchString(id) {
		t.Errorf("id = %q; a tampered file must get replaced by a fresh UUID", id)
	}
}

func TestForgetDeletesTheIDAndTheQueue(t *testing.T) {
	fsys := system.NewMemFS()
	dir := "/s/telemetry"
	_, _ = EnsureInstallID(fsys, dir)
	_ = fsys.WriteFile(filepath.Join(dir, QueueFile), []byte("{}\n"), 0o600)
	if err := Forget(fsys, dir); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	for _, f := range []string{InstallIDFile, QueueFile} {
		if _, err := fsys.Stat(filepath.Join(dir, f)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived Forget: %v", f, err)
		}
	}
	if err := Forget(fsys, dir); err != nil {
		t.Errorf("second Forget: %v; want nil when nothing remains", err)
	}
}
