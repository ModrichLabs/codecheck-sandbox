package guest

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

type tarEntry struct {
	name, body, link string
	typ              byte
	mode             int64
}

func pluginTar(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ, mode := e.typ, e.mode
		if typ == 0 {
			typ = tar.TypeReg
		}
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Mode: mode, Typeflag: typ, Linkname: e.link}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	return buf.Bytes()
}

func goodPlugin(t *testing.T) []byte {
	return pluginTar(t,
		tarEntry{name: ".claude-plugin/", typ: tar.TypeDir, mode: 0o755},
		tarEntry{name: ".claude-plugin/plugin.json", body: `{"name":"codecheck"}`},
		tarEntry{name: "skills/codecheck/SKILL.md", body: "---\nname: codecheck\n---\n"},
		tarEntry{name: "LICENSE.txt", body: "MIT"},
	)
}

func inputsFixture(t *testing.T, plugin []byte) Inputs {
	t.Helper()
	dir := t.TempDir()
	claude := filepath.Join(dir, "claude")
	initBin := filepath.Join(dir, "cc-guestinit")
	os.WriteFile(claude, []byte("\x7fELF claude"), 0o755)
	os.WriteFile(initBin, []byte("\x7fELF init"), 0o755)
	return Inputs{Claude: claude, GuestInit: initBin, CACert: []byte("-----BEGIN CERTIFICATE-----\n"), Plugin: plugin}
}

func TestStageInputsLaysOutTheNamesBuildExpects(t *testing.T) {
	dir := t.TempDir()
	if err := StageInputs(dir, inputsFixture(t, goodPlugin(t))); err != nil {
		t.Fatalf("StageInputs: %v", err)
	}
	for _, name := range []string{"claude", "cc-guestinit", "cc-claude", "cc-selftest", "ca.crt",
		"plugin/.claude-plugin/plugin.json", "plugin/skills/codecheck/SKILL.md", "plugin/LICENSE.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s missing: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "cc-claude")); !strings.Contains(string(b), "--restricted") {
		t.Error("cc-claude isn't the embedded script")
	}
}

func TestStageInputsRefusesAnUnsafePlugin(t *testing.T) {
	for name, e := range map[string]tarEntry{
		"symlink":  {name: "skills/leak", link: "/etc/shadow", typ: tar.TypeSymlink},
		"hardlink": {name: "skills/leak", link: "LICENSE.txt", typ: tar.TypeLink},
		"absolute": {name: "/etc/cron.d/x", body: "x"},
		"parent":   {name: "../escape", body: "x"},
		"device":   {name: "skills/dev", typ: tar.TypeChar},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := StageInputs(dir, inputsFixture(t, pluginTar(t, e))); err == nil {
				t.Error("StageInputs accepted it")
			}
		})
	}
}

func TestStageInputsCapsThePlugin(t *testing.T) {
	big := tarEntry{name: "skills/big", body: strings.Repeat("x", int(maxPluginBytes)+1)}
	if err := StageInputs(t.TempDir(), inputsFixture(t, pluginTar(t, big))); err == nil {
		t.Error("StageInputs accepted a plugin past its cap")
	}
}

// A real disk, read back with the real debugfs.
func TestMakeInputsDisk(t *testing.T) {
	if _, err := lookTool("mkfs.ext4"); err != nil {
		t.Skip("no mkfs.ext4 here")
	}
	debugfs, err := lookTool("debugfs")
	if err != nil {
		t.Skip("no debugfs here")
	}
	work := t.TempDir()
	disk := filepath.Join(work, "inputs.ext4")
	if err := MakeInputs(context.Background(), system.ExecRunner{}, work, disk, inputsFixture(t, goodPlugin(t))); err != nil {
		t.Fatalf("MakeInputs: %v", err)
	}
	res, err := system.ExecRunner{}.Run(context.Background(), system.Cmd{Name: debugfs, Args: []string{"-R", "cat /plugin/LICENSE.txt", disk}})
	if err != nil || string(res.Stdout) != "MIT" {
		t.Errorf("debugfs read %q, %v; want the plugin's licence", res.Stdout, err)
	}
	res, _ = system.ExecRunner{}.Run(context.Background(), system.Cmd{Name: debugfs, Args: []string{"-R", "ls -p /", disk}})
	for _, name := range []string{"/claude/", "/cc-guestinit/", "/cc-claude/", "/cc-selftest/", "/ca.crt/", "/plugin/"} {
		if !strings.Contains(string(res.Stdout), name) {
			t.Errorf("disk root lacks %s:\n%s", name, res.Stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "inputs")); !os.IsNotExist(err) {
		t.Errorf("the staging folder stayed: %v", err)
	}
}
