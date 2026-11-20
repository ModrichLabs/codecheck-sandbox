package system

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// waitClosed reads ch until it closes and returns the errors it carried.
func waitClosed(t *testing.T, ch <-chan error) []error {
	t.Helper()
	var errs []error
	timeout := time.After(2 * time.Second)
	for {
		select {
		case err, ok := <-ch:
			if !ok {
				return errs
			}
			errs = append(errs, err)
		case <-timeout:
			t.Fatal("keep-alive channel never closed")
		}
	}
}

func TestKeepAliveRefreshesOnEachTick(t *testing.T) {
	sudo := Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}
	f := &FakeRunner{}
	for range 3 {
		f.On(sudo.Path, "-n", "-v").Return(Result{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := KeepAlive(ctx, f, sudo, tick)

	// The tick channel has no buffer, so each send waits until the goroutine
	// takes it, and a received tick always finishes its refresh.
	for range 3 {
		tick <- time.Time{}
	}
	cancel()
	if errs := waitClosed(t, done); len(errs) != 0 {
		t.Errorf("errors = %v, want none", errs)
	}
	want := Cmd{Name: sudo.Path, Args: []string{"-n", "-v"}}
	calls := f.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	for _, c := range calls {
		if !reflect.DeepEqual(c, want) {
			t.Errorf("call = %#v, want %#v", c, want)
		}
	}
}

func TestKeepAliveReportsFirstFailureAndStops(t *testing.T) {
	sudo := Tool{Kind: ToolSudoRS, Path: "/usr/bin/sudo"}
	f := &FakeRunner{}
	f.On(sudo.Path, "-n", "-v").Return(Result{Code: 1, Stderr: []byte("sudo: a password is required\n")})
	tick := make(chan time.Time, 1)
	tick <- time.Time{}

	errs := waitClosed(t, KeepAlive(context.Background(), f, sudo, tick))
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "a password is required") {
		t.Errorf("errors = %v, want one naming the sudo message", errs)
	}
	if !errors.Is(errs[0], ErrLoginExpired) {
		t.Errorf("err = %v, want ErrLoginExpired", errs[0])
	}
}

func TestKeepAliveReportsRunnerErrors(t *testing.T) {
	sudo := Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}
	boom := errors.New("boom")
	f := &FakeRunner{}
	f.On(sudo.Path).Fail(boom)
	tick := make(chan time.Time, 1)
	tick <- time.Time{}

	errs := waitClosed(t, KeepAlive(context.Background(), f, sudo, tick))
	if len(errs) != 1 || !errors.Is(errs[0], boom) {
		t.Errorf("errors = %v, want [boom]", errs)
	}
}

func TestKeepAliveStaysQuietWhenCancelledMidTick(t *testing.T) {
	sudo := Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"}
	// With ctx already ended and a tick waiting, select picks either case at
	// random; neither may report an error.
	for range 50 {
		f := &FakeRunner{}
		f.On(sudo.Path).Fail(context.Canceled)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		tick := make(chan time.Time, 1)
		tick <- time.Time{}
		if errs := waitClosed(t, KeepAlive(ctx, f, sudo, tick)); len(errs) != 0 {
			t.Fatalf("errors = %v, want none after cancel", errs)
		}
	}
}

func TestKeepAliveDoasChecksWithTrue(t *testing.T) {
	doas := Tool{Kind: ToolDoas, Path: "/usr/bin/doas"}
	f := &FakeRunner{}
	f.On(doas.Path, "-n", "true").Return(Result{})
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := KeepAlive(ctx, f, doas, tick)
	tick <- time.Time{}
	cancel()
	waitClosed(t, done)
	if len(f.Pending()) != 0 {
		t.Errorf("doas -n true never ran: %v", f.Calls())
	}
}

func TestKeepAliveRun0DoesNothing(t *testing.T) {
	f := &FakeRunner{} // any call would panic
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := KeepAlive(ctx, f, Tool{Kind: ToolRun0, Path: "/usr/bin/run0"}, tick)
	select {
	case tick <- time.Time{}:
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if errs := waitClosed(t, done); len(errs) != 0 {
		t.Errorf("errors = %v, want none", errs)
	}
}

func TestStartKeepAliveClosesWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := StartKeepAlive(ctx, &FakeRunner{}, Tool{Kind: ToolSudo, Path: "/usr/bin/sudo"})
	cancel()
	if errs := waitClosed(t, done); len(errs) != 0 {
		t.Errorf("errors = %v, want none", errs)
	}
}
