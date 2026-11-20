package system

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestExecRunnerStreamsToWriters(t *testing.T) {
	var out, errOut bytes.Buffer
	res := runOK(t, Cmd{
		Name:   "sh",
		Args:   []string{"-c", "printf out; printf err >&2; exit 3"},
		Stdout: &out, Stderr: &errOut,
	})
	if out.String() != "out" || errOut.String() != "err" {
		t.Errorf("writers got %q and %q, want out and err", out.String(), errOut.String())
	}
	if len(res.Stdout) != 0 || len(res.Stderr) != 0 {
		t.Errorf("Result kept output %q / %q; a streamed command keeps none", res.Stdout, res.Stderr)
	}
	if res.Code != 3 {
		t.Errorf("Code = %d, want 3", res.Code)
	}
}

func TestExecRunnerRunsInDir(t *testing.T) {
	dir := t.TempDir()
	res := runOK(t, Cmd{Name: "pwd", Dir: dir})
	if got := strings.TrimSpace(string(res.Stdout)); got != dir {
		t.Errorf("pwd = %q, want %q", got, dir)
	}
}

func TestPrivRunnerRefusesFieldsItWouldDrop(t *testing.T) {
	p := PrivRunner{Inner: &FakeRunner{}, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}
	for name, c := range map[string]Cmd{
		"stdout": {Name: "true", Root: true, Stdout: &bytes.Buffer{}},
		"stderr": {Name: "true", Root: true, Stderr: &bytes.Buffer{}},
		"dir":    {Name: "true", Root: true, Dir: "/tmp"},
		"chroot": {Name: "true", Root: true, Chroot: "/srv"},
	} {
		if _, err := p.Run(context.Background(), c); err == nil {
			t.Errorf("%s: PrivRunner accepted a root command it can't honour", name)
		}
	}
}

func TestExecRunnerAppliesChroot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may chroot; this test checks that an unprivileged chroot fails")
	}
	// Without CAP_SYS_CHROOT the kernel refuses even chroot("/"), which proves
	// the runner asked for it.
	_, err := ExecRunner{}.Run(context.Background(), Cmd{Name: "/bin/true", Chroot: "/"})
	if !errors.Is(err, syscall.EPERM) {
		t.Errorf("Run with Chroot = %v, want EPERM", err)
	}
}
