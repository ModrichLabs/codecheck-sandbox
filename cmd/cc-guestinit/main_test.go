package main

import (
	"bytes"
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRefusesAnythingButPID1(t *testing.T) {
	var console bytes.Buffer
	rebooted := false
	code := run(4242, &console, func() { rebooted = true })
	if code == 0 || rebooted {
		t.Errorf("run as pid 4242 = %d, rebooted %v; want a refusal and no reboot", code, rebooted)
	}
	if !strings.Contains(console.String(), "PID 1") {
		t.Errorf("console = %q, want an explanation", console.String())
	}
}

func TestRunStartsTheInitAsPID1(t *testing.T) {
	started := false
	if code := run(1, &bytes.Buffer{}, func() { started = true }); !started || code != 0 {
		t.Errorf("run as pid 1 = %d, started %v", code, started)
	}
}

// The guest boots this binary straight from the kernel, with no dynamic
// loader to rely on in the builder's initramfs.
func TestBinaryBuildsStatic(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	out := filepath.Join(t.TempDir(), "cc-guestinit")
	cmd := exec.Command("go", "build", "-trimpath", "-o", out, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	f, err := elf.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Error("the binary asks for a dynamic loader")
		}
	}
	if libs, _ := f.ImportedLibraries(); len(libs) > 0 {
		t.Errorf("the binary links %v", libs)
	}
}
