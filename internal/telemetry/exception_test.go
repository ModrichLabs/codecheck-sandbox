package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestExceptionKeepsNeitherPathNorMessage(t *testing.T) {
	err := fmt.Errorf("reading /home/x/secret: %w", &fs.PathError{Op: "open", Path: "/home/x/secret", Err: fs.ErrNotExist})
	ex := NewException(err, "")
	props, perr := Properties(ex)
	if perr != nil {
		t.Fatalf("Properties: %v", perr)
	}
	out, _ := json.Marshal(props)
	for _, bad := range []string{"/home/x/secret", "reading", "open"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("exception leaked %q:\n%s", bad, out)
		}
	}
	if props["kind"] != "not_exist" {
		t.Errorf("kind = %v, want not_exist", props["kind"])
	}
}

func TestExceptionKinds(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{context.Canceled, "canceled"},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), "deadline_exceeded"},
		{fs.ErrPermission, "permission"},
		{fmt.Errorf("step: %w", system.ErrLoginExpired), "login_expired"},
		{system.ErrNoPrivilegeTool, "no_privilege_tool"},
		{step.ErrDeclined, "declined"},
		{step.ErrStillFailing, "still_failing"},
		{step.ErrBusy, "busy"},
		{step.ErrPathNotAllowed, "path_not_allowed"},
		{errors.New("something odd at /home/alice"), "unknown"},
	}
	for _, tt := range tests {
		if got := KindOf(tt.err); string(got) != tt.want {
			t.Errorf("KindOf(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

// stackErr carries the call stack from where it arose.
type stackErr struct{ pcs []uintptr }

func (e stackErr) Error() string      { return "boom at /home/alice" }
func (e stackErr) Callers() []uintptr { return e.pcs }

func newStackErr() error {
	pcs := make([]uintptr, 32)
	return stackErr{pcs: pcs[:runtime.Callers(1, pcs)]}
}

func TestExceptionWithoutAnErrorStackSendsNone(t *testing.T) {
	// The reporting site's own frames would only mislead.
	if ex := NewException(errors.New("x"), ""); len(ex.Stack) != 0 {
		t.Errorf("stack = %+v, want none for an error without frames", ex.Stack)
	}
}

func TestMainPackageFramesSurvive(t *testing.T) {
	for _, s := range []string{"cmd/codecheck-sandbox.main", "cmd/cc-launch.run"} {
		if Symbol(s).value() != s {
			t.Errorf("Symbol(%q) encodes as %v", s, Symbol(s).value())
		}
	}
	if f := "cmd/codecheck-sandbox/main.go"; ModuleFile(f).value() != f {
		t.Errorf("ModuleFile(%q) encodes as %v", f, ModuleFile(f).value())
	}
}

func TestExceptionStackStaysInsideTheModule(t *testing.T) {
	ex := NewException(fmt.Errorf("wrapped: %w", newStackErr()), "")
	if len(ex.Stack) == 0 {
		t.Fatal("no stack frames")
	}
	for _, f := range ex.Stack {
		fn, file := string(f.Function), string(f.File)
		if !strings.HasPrefix(fn, "internal/") || !strings.HasPrefix(file, "internal/") || !strings.HasSuffix(file, ".go") {
			t.Errorf("frame %q in %q escapes the module root", fn, file)
		}
		if f.Function.value() != fn || f.File.value() != file {
			t.Errorf("frame %q / %q fails its own validation", fn, file)
		}
	}
	if string(ex.Stack[0].File) != "internal/telemetry/exception_test.go" || string(ex.Stack[0].Function) != "internal/telemetry.newStackErr" {
		t.Errorf("first frame = %+v, want newStackErr, where the error arose", ex.Stack[0])
	}
}

func TestExceptionStepAndName(t *testing.T) {
	RegisterStepIDs("jail-user")
	ex := NewException(step.ErrDeclined, "jail-user")
	props, _ := Properties(ex)
	if props["step"] != "jail-user" || ex.EventName() != "$exception" {
		t.Errorf("props = %v, name = %q", props, ex.EventName())
	}
}

func TestFrameTypesRejectOutsideValues(t *testing.T) {
	for _, s := range []string{"/home/alice/x.go", "main.main", "github.com/acme/x.F", "internal/../../etc/passwd"} {
		if Symbol(s).value() != "other" || ModuleFile(s).value() != "other" {
			t.Errorf("%q passed a frame type", s)
		}
	}
}
