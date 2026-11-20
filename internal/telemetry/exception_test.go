package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

func TestExceptionStackStaysInsideTheModule(t *testing.T) {
	ex := NewException(errors.New("x"), "")
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
	if string(ex.Stack[0].File) != "internal/telemetry/exception_test.go" {
		t.Errorf("first frame file = %q, want this test file", ex.Stack[0].File)
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
