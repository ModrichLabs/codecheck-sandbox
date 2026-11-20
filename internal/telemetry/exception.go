package telemetry

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path"
	"regexp"
	"runtime"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// modulePrefix starts every function name inside this module.
const modulePrefix = "github.com/ModrichLabs/codecheck-sandbox/"

// maxFrames caps the stack an exception carries.
const maxFrames = 20

// Exception reports an error (spec §11, errors): its kind, the failing step
// and the module's own stack, never the message.
type Exception struct {
	Kind  ErrorKind    `prop:"kind"`
	Step  StepID       `prop:"step"`
	Stack []StackFrame `prop:"stack"`
}

func (Exception) EventName() string { return "$exception" }

// StackFrame names one call inside the module.
type StackFrame struct {
	Function Symbol     `prop:"function"`
	File     ModuleFile `prop:"filename"`
	Line     LineNo     `prop:"lineno"`
}

func (f StackFrame) encode() map[string]any {
	return map[string]any{"function": f.Function.value(), "filename": f.File.value(), "lineno": f.Line.value()}
}

// Symbol holds a function name relative to the module root, such as
// internal/step.(*engine).runStep.
type Symbol string

var symbolPattern = regexp.MustCompile(`^(internal|cmd)/[a-z0-9_/-]+\.[A-Za-z0-9_.()*\[\]-]+$`)

func (s Symbol) value() any {
	if symbolPattern.MatchString(string(s)) && !strings.Contains(string(s), "..") {
		return string(s)
	}
	return other
}

// ModuleFile holds a source file relative to the module root.
type ModuleFile string

var moduleFilePattern = regexp.MustCompile(`^(internal|cmd)(/[a-z0-9_-]+)+\.go$`)

func (f ModuleFile) value() any {
	if moduleFilePattern.MatchString(string(f)) {
		return string(f)
	}
	return other
}

// LineNo holds a source line number.
type LineNo int

func (n LineNo) value() any { return int(n) }

// ErrorKind names an error by its sentinel or type from kindRules, never by
// its message.
type ErrorKind string

func (k ErrorKind) value() any {
	if k == "unknown" {
		return "unknown"
	}
	for _, r := range kindRules {
		if string(k) == r.name {
			return r.name
		}
	}
	return other
}

type kindRule struct {
	name  string
	match func(error) bool
}

func is(target error) func(error) bool { return func(err error) bool { return errors.Is(err, target) } }

func as[T error]() func(error) bool {
	return func(err error) bool {
		var t T
		return errors.As(err, &t)
	}
}

// kindRules maps errors to kinds, most specific first. The project's own
// sentinels come before the generic ones they may wrap.
var kindRules = []kindRule{
	{"login_expired", is(system.ErrLoginExpired)},
	{"no_privilege_tool", is(system.ErrNoPrivilegeTool)},
	{"root_needed", is(system.ErrRootNeeded)},
	{"declined", is(step.ErrDeclined)},
	{"still_failing", is(step.ErrStillFailing)},
	{"busy", is(step.ErrBusy)},
	{"no_manifest", is(step.ErrNoManifest)},
	{"manifest_version", is(step.ErrManifestVersion)},
	{"path_not_allowed", is(step.ErrPathNotAllowed)},
	{"name_not_allowed", is(step.ErrNameNotAllowed)},
	{"canceled", is(context.Canceled)},
	{"deadline_exceeded", is(context.DeadlineExceeded)},
	{"not_exist", is(fs.ErrNotExist)},
	{"permission", is(fs.ErrPermission)},
	{"exec_not_found", as[*exec.Error]()},
	{"exit_status", as[*exec.ExitError]()},
}

// KindOf names err by the first matching rule, or "unknown".
func KindOf(err error) ErrorKind {
	for _, r := range kindRules {
		if r.match(err) {
			return ErrorKind(r.name)
		}
	}
	return "unknown"
}

// stackCarrier marks an error that recorded the call stack where it arose.
// Errors built that way give exceptions a useful stack; the reporting site's
// own frames (a CLI wrapper, the engine's hook) would only mislead.
type stackCarrier interface {
	Callers() []uintptr
}

// NewException builds an exception event for err, failing in the step with
// the given ID (empty when no step ran). When some error in err's chain
// carries its own stack, the event keeps that stack's frames inside the
// module, with paths cut to the module root; otherwise it sends no stack.
func NewException(err error, stepID string) Exception {
	ex := Exception{Kind: KindOf(err), Step: StepID(stepID)}
	var sc stackCarrier
	if errors.As(err, &sc) {
		ex.Stack = moduleFrames(sc.Callers())
	}
	return ex
}

func moduleFrames(pcs []uintptr) []StackFrame {
	if len(pcs) == 0 {
		return nil
	}
	frames := runtime.CallersFrames(pcs)
	var stack []StackFrame
	for {
		fr, more := frames.Next()
		if rel, ok := strings.CutPrefix(fr.Function, modulePrefix); ok && len(stack) < maxFrames {
			stack = append(stack, StackFrame{
				Function: Symbol(rel),
				File:     ModuleFile(packageOf(rel) + "/" + path.Base(fr.File)),
				Line:     LineNo(fr.Line),
			})
		}
		if !more {
			break
		}
	}
	return stack
}

// packageOf cuts a function name such as internal/step.(*engine).run to its
// package path, internal/step. File paths come from here rather than from the
// build machine's absolute paths.
func packageOf(fn string) string {
	slash := strings.LastIndexByte(fn, '/')
	dot := strings.IndexByte(fn[slash+1:], '.')
	if dot < 0 {
		return fn
	}
	return fn[:slash+1+dot]
}
