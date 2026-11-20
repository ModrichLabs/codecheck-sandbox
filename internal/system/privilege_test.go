package system

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// fakePath answers exec.LookPath-style queries from a fixed table.
func fakePath(found map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if p, ok := found[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
}

func TestDetectTool(t *testing.T) {
	tests := []struct {
		name    string
		path    map[string]string
		version string // sudo --version stdout, when sudo exists
		want    Tool
	}{
		{
			name:    "classic sudo",
			path:    map[string]string{"sudo": "/usr/bin/sudo", "run0": "/usr/bin/run0"},
			version: "Sudo version 1.9.17p2\nSudoers policy plugin version 1.9.17p2\n",
			want:    Tool{Kind: ToolSudo, Path: "/usr/bin/sudo", Version: "Sudo version 1.9.17p2"},
		},
		{
			name:    "sudo-rs installed as sudo",
			path:    map[string]string{"sudo": "/usr/bin/sudo"},
			version: "sudo-rs 0.2.8\n",
			want:    Tool{Kind: ToolSudoRS, Path: "/usr/bin/sudo", Version: "sudo-rs 0.2.8"},
		},
		{
			name: "sudo-rs under its own name",
			path: map[string]string{"sudo-rs": "/usr/bin/sudo-rs", "doas": "/usr/bin/doas"},
			want: Tool{Kind: ToolSudoRS, Path: "/usr/bin/sudo-rs"},
		},
		{
			name: "doas",
			path: map[string]string{"doas": "/usr/bin/doas", "run0": "/usr/bin/run0"},
			want: Tool{Kind: ToolDoas, Path: "/usr/bin/doas"},
		},
		{
			name: "run0",
			path: map[string]string{"run0": "/usr/bin/run0"},
			want: Tool{Kind: ToolRun0, Path: "/usr/bin/run0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &FakeRunner{}
			if p, ok := tt.path["sudo"]; ok {
				f.On(p, "--version").Return(Result{Stdout: []byte(tt.version)})
			}
			got, err := DetectTool(context.Background(), f, fakePath(tt.path))
			if err != nil {
				t.Fatalf("DetectTool: %v", err)
			}
			if got != tt.want {
				t.Errorf("DetectTool = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetectToolNoneFound(t *testing.T) {
	_, err := DetectTool(context.Background(), &FakeRunner{}, fakePath(nil))
	if !errors.Is(err, ErrNoPrivilegeTool) {
		t.Errorf("err = %v, want ErrNoPrivilegeTool", err)
	}
}

func TestPrivRunnerArgvPerTool(t *testing.T) {
	cmd := []string{"--", "nft", "-j", "list", "ruleset"}
	tests := []struct {
		tool Tool
		args []string
	}{
		{Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}, append([]string{"-n"}, cmd...)},
		{Tool{Kind: ToolSudoRS, Path: "/usr/bin/sudo-rs"}, append([]string{"-n"}, cmd...)},
		{Tool{Kind: ToolDoas, Path: "/usr/bin/doas"}, append([]string{"-n"}, cmd...)},
		// run0 starts a fresh environment, so it gets LC_ALL=C explicitly.
		{Tool{Kind: ToolRun0, Path: "/usr/bin/run0"}, append([]string{"--no-ask-password", "--setenv=LC_ALL=C"}, cmd...)},
	}
	for _, tt := range tests {
		tool := tt.tool
		t.Run(string(tool.Kind), func(t *testing.T) {
			f := &FakeRunner{}
			f.On(tool.Path).Return(Result{})
			p := PrivRunner{Inner: f, Tool: tool}
			in := Cmd{Name: "nft", Args: []string{"-j", "list", "ruleset"}, Stdin: []byte("x"), Root: true}
			if _, err := p.Run(context.Background(), in); err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := Cmd{Name: tool.Path, Args: tt.args, Stdin: []byte("x")}
			if got := f.Calls(); !reflect.DeepEqual(got, []Cmd{want}) {
				t.Errorf("calls = %#v, want %#v", got, []Cmd{want})
			}
		})
	}
}

func TestPrivRunnerReportsAMissingLoginAsExpired(t *testing.T) {
	replies := map[ToolKind]string{
		ToolSudo:   "sudo: a password is required\n",
		ToolSudoRS: "sudo-rs: interactive authentication is required\n",
		ToolDoas:   "doas: Authentication required\n",
		ToolRun0:   "Failed to start transient service unit: Access denied as the requested operation requires interactive authentication.\n",
	}
	for kind, stderr := range replies {
		f := &FakeRunner{}
		f.On("/bin/" + string(kind)).Return(Result{Code: 1, Stderr: []byte(stderr)})
		p := PrivRunner{Inner: f, Tool: Tool{Kind: kind, Path: "/bin/" + string(kind)}}
		_, err := p.Run(context.Background(), Cmd{Name: "nft", Args: []string{"list", "ruleset"}, Root: true})
		if !errors.Is(err, ErrLoginExpired) {
			t.Errorf("%s: err = %v, want ErrLoginExpired so the engine hands over", kind, err)
		}
	}
	// A root command's own failure stays a plain non-zero exit.
	f := &FakeRunner{}
	f.On("/bin/sudo").Return(Result{Code: 1, Stderr: []byte("Error: No such file or directory\n")})
	res, err := (PrivRunner{Inner: f, Tool: Tool{Kind: ToolSudo, Path: "/bin/sudo"}}).Run(context.Background(), Cmd{Name: "nft", Root: true})
	if err != nil || res.Code != 1 {
		t.Errorf("Run = %+v, %v; want the command's own exit 1", res, err)
	}
}

func TestPrivRunnerPassesUserCommandsThrough(t *testing.T) {
	f := &FakeRunner{}
	f.On("ip").Return(Result{})
	in := Cmd{Name: "ip", Args: []string{"-j", "link"}, Env: []string{"A=b"}}
	if _, err := (PrivRunner{Inner: f, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}).Run(context.Background(), in); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := f.Calls(); !reflect.DeepEqual(got, []Cmd{in}) {
		t.Errorf("calls = %#v, want %#v", got, []Cmd{in})
	}
}

func TestPrivRunnerRefusesEnvOnRootCommands(t *testing.T) {
	p := PrivRunner{Inner: &FakeRunner{}, Tool: Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}}
	_, err := p.Run(context.Background(), Cmd{Name: "true", Env: []string{"A=b"}, Root: true})
	if err == nil {
		t.Fatal("expected an error: the privilege tool would drop Env")
	}
}

func TestWriteRootFile(t *testing.T) {
	f := &FakeRunner{}
	f.On("install").Return(Result{})
	f.On("dd").Return(Result{})
	data := []byte("payload")

	if err := WriteRootFile(context.Background(), f, "/etc/sudoers.d/codecheck-sandbox", data, 0o440); err != nil {
		t.Fatalf("WriteRootFile: %v", err)
	}
	// dd prints nothing, unlike tee, so the contents never come back as
	// command output that a log could record.
	want := []Cmd{
		{Name: "install", Args: []string{"-m", "0440", "-T", "/dev/null", "/etc/sudoers.d/codecheck-sandbox"}, Root: true},
		{Name: "dd", Args: []string{"of=/etc/sudoers.d/codecheck-sandbox", "status=none"}, Stdin: data, Root: true},
	}
	if got := f.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls =\n%#v\nwant\n%#v", got, want)
	}
}

func TestWriteRootFileReportsFailure(t *testing.T) {
	f := &FakeRunner{}
	f.On("install").Return(Result{Code: 1, Stderr: []byte("install: cannot create: Read-only file system\n")})
	err := WriteRootFile(context.Background(), f, "/etc/x", []byte("a"), os.FileMode(0o644))
	if err == nil || !strings.Contains(err.Error(), "Read-only file system") {
		t.Errorf("err = %v, want one carrying install's stderr", err)
	}
	if len(f.Calls()) != 1 {
		t.Errorf("tee ran after install failed: %v", f.Calls())
	}
}
