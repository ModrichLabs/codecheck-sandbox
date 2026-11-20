package assets

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guestScripts lists every script in assets/guest: the files that start
// with a shebang.
func guestScripts(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := fs.WalkDir(Guest, "guest", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := Guest.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(string(b), "#!") {
			out[path] = b
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 2 {
		t.Fatalf("found %d guest scripts, want cc-claude and cc-selftest at least", len(out))
	}
	return out
}

// writeScripts copies the scripts to a temp folder for tools that need files.
func writeScripts(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, b := range guestScripts(t) {
		p := filepath.Join(dir, filepath.Base(name))
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func TestGuestScriptsParse(t *testing.T) {
	for _, p := range writeScripts(t) {
		if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
			t.Errorf("%s: bash -n: %v\n%s", filepath.Base(p), err, out)
		}
	}
}

func TestGuestScriptsPassShellcheck(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed; the scripts went unchecked")
	}
	// A version manager's shim can sit on PATH without a working tool behind it.
	if err := exec.Command(sc, "--version").Run(); err != nil {
		t.Skipf("shellcheck at %s doesn't run (%v); the scripts went unchecked", sc, err)
	}
	for _, p := range writeScripts(t) {
		if out, err := exec.Command(sc, "--shell=bash", "--severity=style", p).CombinedOutput(); err != nil {
			t.Errorf("%s: shellcheck:\n%s", filepath.Base(p), out)
		}
	}
}

func TestGuestScriptsUseStrictBash(t *testing.T) {
	for name, b := range guestScripts(t) {
		if !strings.HasPrefix(string(b), "#!/bin/bash\n") {
			t.Errorf("%s: want #!/bin/bash, the guest's known shell", name)
		}
	}
}
