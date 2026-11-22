package system

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestRedactingRunnerRedactsStreamedStderr(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	var errOut bytes.Buffer
	_, err := r.Runner(ExecRunner{}).Run(context.Background(), Cmd{
		Name:   "sh",
		Args:   []string{"-c", `printf 'bad key %s\n' "$1" >&2`, "sh", testKey},
		Stderr: &errOut,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(errOut.String(), testKey) || !strings.Contains(errOut.String(), "bad key") {
		t.Errorf("streamed stderr = %q, want it redacted", errOut.String())
	}
}

func TestExecRunnerReadsStdinFromAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(p, []byte("from a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res := runOK(t, Cmd{Name: "cat", StdinFile: f})
	if string(res.Stdout) != "from a file" {
		t.Errorf("stdout = %q", res.Stdout)
	}
}

func TestPrivRunnerRefusesAStdinFile(t *testing.T) {
	p := PrivRunner{Inner: &FakeRunner{}, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}
	if _, err := p.Run(context.Background(), Cmd{Name: "true", Root: true, StdinFile: os.Stdin}); err == nil {
		t.Error("PrivRunner accepted a root command it can't honour")
	}
}

// stderrRunner writes a scripted stderr to the command's writer.
type stderrRunner struct {
	stderr string
	code   int
	got    Cmd
}

func (s *stderrRunner) Run(_ context.Context, c Cmd) (Result, error) {
	s.got = c
	if c.Stderr != nil {
		c.Stderr.Write([]byte(s.stderr))
	}
	return Result{Code: s.code}, nil
}

func TestRunStreamingWrapsAndStreams(t *testing.T) {
	inner := &stderrRunner{stderr: "console line\n", code: 6}
	p := PrivRunner{Inner: inner, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}
	var console bytes.Buffer
	res, err := p.RunStreaming(context.Background(), Cmd{Name: "/usr/local/lib/codecheck-sandbox/bin/cc-launch", Args: []string{"build", "x"}}, &console)
	if err != nil || res.Code != 6 {
		t.Fatalf("RunStreaming = %+v, %v", res, err)
	}
	want := []string{"-n", "--", "/usr/local/lib/codecheck-sandbox/bin/cc-launch", "build", "x"}
	if inner.got.Name != "/usr/bin/sudo" || !reflect.DeepEqual(inner.got.Args, want) || inner.got.Root {
		t.Errorf("ran %s %v", inner.got.Name, inner.got.Args)
	}
	if console.String() != "console line\n" {
		t.Errorf("console = %q", console.String())
	}
}

func TestRunStreamingSpotsAnExpiredLogin(t *testing.T) {
	inner := &stderrRunner{stderr: "sudo: a password is required\n", code: 1}
	p := PrivRunner{Inner: inner, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}
	if _, err := p.RunStreaming(context.Background(), Cmd{Name: "cc-launch"}, &bytes.Buffer{}); !errors.Is(err, ErrLoginExpired) {
		t.Errorf("err = %v, want ErrLoginExpired", err)
	}
}

func TestRunStreamingRefusesEnv(t *testing.T) {
	p := PrivRunner{Inner: &stderrRunner{}, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}
	if _, err := p.RunStreaming(context.Background(), Cmd{Name: "x", Env: []string{"A=b"}}, &bytes.Buffer{}); err == nil {
		t.Error("RunStreaming accepted Env it would drop")
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
