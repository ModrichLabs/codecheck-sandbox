package guest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// cpioEntry is one parsed newc entry.
type cpioEntry struct {
	name             string
	mode             uint64
	uid, gid         uint64
	rdevMaj, rdevMin uint64
	data             []byte
}

// parseNewc reads a newc archive, failing the test on any malformed field.
func parseNewc(t *testing.T, b []byte) []cpioEntry {
	t.Helper()
	var out []cpioEntry
	pad := func(n int) int { return (4 - n%4) % 4 }
	for off := 0; ; {
		if len(b) < off+110 || string(b[off:off+6]) != "070701" {
			t.Fatalf("no newc header at %d", off)
		}
		field := func(i int) uint64 {
			v, err := strconv.ParseUint(string(b[off+6+8*i:off+14+8*i]), 16, 32)
			if err != nil {
				t.Fatalf("header field %d at %d: %v", i, off, err)
			}
			return v
		}
		e := cpioEntry{mode: field(1), uid: field(2), gid: field(3), rdevMaj: field(9), rdevMin: field(10)}
		size, nameSize := int(field(6)), int(field(11))
		nameStart := off + 110
		e.name = string(b[nameStart : nameStart+nameSize-1])
		if b[nameStart+nameSize-1] != 0 {
			t.Fatalf("%s: name not NUL-terminated", e.name)
		}
		dataStart := nameStart + nameSize + pad(110+nameSize)
		e.data = b[dataStart : dataStart+size]
		off = dataStart + size + pad(size)
		if e.name == "TRAILER!!!" {
			if off != len(b) {
				t.Errorf("%d bytes after the trailer", len(b)-off)
			}
			return out
		}
		out = append(out, e)
	}
}

func TestInitramfs(t *testing.T) {
	init := []byte("\x7fELF pretend cc-guestinit, 31 b")
	b := Initramfs(init)
	entries := parseNewc(t, b)
	want := []struct {
		name string
		mode uint64
	}{
		{"dev", 0o040755},
		{"dev/console", 0o020600},
		{"init", 0o100755},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v", entries)
	}
	for i, w := range want {
		e := entries[i]
		if e.name != w.name || e.mode != w.mode || e.uid != 0 || e.gid != 0 {
			t.Errorf("entry %d = %s mode %o uid %d gid %d; want %s mode %o, root", i, e.name, e.mode, e.uid, e.gid, w.name, w.mode)
		}
	}
	if c := entries[1]; c.rdevMaj != 5 || c.rdevMin != 1 {
		t.Errorf("dev/console = %d:%d, want 5:1", c.rdevMaj, c.rdevMin)
	}
	if !bytes.Equal(entries[2].data, init) {
		t.Errorf("init holds %q", entries[2].data)
	}
	if !bytes.Equal(b, Initramfs(init)) {
		t.Error("two builds of the same init differ")
	}
}

// The real cpio reads the archive the same way.
func TestInitramfsReadsWithCpio(t *testing.T) {
	cpio, err := exec.LookPath("cpio")
	if err != nil {
		t.Skip("no cpio here")
	}
	p := filepath.Join(t.TempDir(), "init.cpio")
	if err := os.WriteFile(p, Initramfs([]byte("abc")), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	res, err := system.ExecRunner{}.Run(context.Background(), system.Cmd{Name: cpio, Args: []string{"-itv", "--quiet"}, StdinFile: f})
	if err != nil || res.Code != 0 {
		t.Fatalf("cpio: %v %s", err, res.Stderr)
	}
	out := string(res.Stdout)
	for _, want := range []string{"drwxr-xr-x", "dev\n", "crw-------", "5,", "dev/console", "-rwxr-xr-x", "init"} {
		if !strings.Contains(out, want) {
			t.Errorf("cpio -itv lacks %q:\n%s", want, out)
		}
	}
}
