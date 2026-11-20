package guestinit

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// recorder stands in for the reboot syscall.
type recorder struct{ calls int }

func (r *recorder) reboot() error { r.calls++; return nil }

func TestRunInitDispatchesTheModeAndReboots(t *testing.T) {
	s := newFakeSys()
	s.devices[ConfigDevice] = block("mode=selftest\nrun_id=s1\n")
	f := &system.FakeRunner{}
	scriptNetwork(f)
	f.On(setprivPath).Return(system.Result{Code: 0})
	var console bytes.Buffer
	r := &recorder{}

	RunInit(context.Background(), Env{Sys: s, Run: f}, newFakeOps(), &console, r.reboot)

	if r.calls != 1 {
		t.Errorf("reboot ran %d times, want 1", r.calls)
	}
	if got := f.Calls(); len(got) != 4 || got[3].Args[len(got[3].Args)-1] != selftestPath {
		t.Errorf("RunInit didn't run the selftest: %+v", got)
	}
	if !strings.Contains(console.String(), "selftest finished: exit 0") {
		t.Errorf("console = %q", console.String())
	}
}

func TestRunInitRebootsAfterABadConfig(t *testing.T) {
	s := newFakeSys()
	s.devices[ConfigDevice] = block("mode=shell\nrun_id=x\n")
	var console bytes.Buffer
	r := &recorder{}

	RunInit(context.Background(), Env{Sys: s, Run: &system.FakeRunner{}}, newFakeOps(), &console, r.reboot)

	if r.calls != 1 {
		t.Errorf("reboot ran %d times, want 1", r.calls)
	}
	if !strings.Contains(console.String(), "mode") {
		t.Errorf("console doesn't explain the failure: %q", console.String())
	}
}

func TestRunInitRebootsAfterAPanic(t *testing.T) {
	s := newFakeSys()
	s.devices[ConfigDevice] = block("mode=review\nrun_id=r1\nmax_budget_usd=5\nscope=full\n")
	// A FakeRunner with nothing scripted panics on the first command.
	var console bytes.Buffer
	r := &recorder{}

	RunInit(context.Background(), Env{Sys: s, Run: &system.FakeRunner{}}, newFakeOps(), &console, r.reboot)

	if r.calls != 1 {
		t.Errorf("reboot ran %d times after a panic, want 1", r.calls)
	}
	if !strings.Contains(console.String(), "panic") {
		t.Errorf("console doesn't report the panic: %q", console.String())
	}
	if s.synced == 0 {
		t.Error("RunInit didn't sync before rebooting")
	}
}
