package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

var pkgPlan = step.Plan{Cmds: []step.PlannedCmd{{Name: "pacman", Args: []string{"-S", "--needed", "--noconfirm", "e2fsprogs"}, Root: true}}, NeedsRoot: true}

// fakeEngine plays a setup run the way step.Run would, asking Confirm and
// Elevate where the real engine does.
func fakeEngine(fail error) RunFunc {
	return func(ctx context.Context, opts step.Options) error {
		send := func(evs ...step.Event) { emit(ctx, opts, evs...) }
		planned := func(id string, p step.Plan) step.Event {
			e := ev(step.StepPlanned, id)
			e.Plan = &p
			return e
		}
		confirm := func(id string, p step.Plan) error {
			if opts.AssumeYes || opts.Confirm == nil {
				return nil
			}
			ok, err := opts.Confirm(ctx, fakeStep{id: id, title: id}, p)
			if err != nil {
				return err
			}
			if !ok {
				return step.ErrDeclined
			}
			return nil
		}
		skipped := ev(step.StepDone, "platform-kvm")
		skipped.Skipped, skipped.Status = true, step.Status{OK: true, Detail: "/dev/kvm usable"}
		send(ev(step.StepStarted, "platform-kvm"), skipped)

		send(ev(step.StepStarted, "components"), planned("components", pkgPlan))
		if err := confirm("components", pkgPlan); err != nil {
			f := ev(step.StepFailed, "components")
			f.Err = err
			send(f)
			return err
		}
		if opts.Elevate != nil {
			send(ev(step.NeedsSudo, "components"))
			if err := opts.Elevate(ctx); err != nil {
				return err
			}
		}
		send(ev(step.StepDone, "components"))

		send(ev(step.StepStarted, "guest-image"))
		for _, f := range []float64{0.05, 0.07, 0.42, 0.43, 1} {
			p := ev(step.Progress, "guest-image")
			p.Message, p.Fraction = "Building the guest root disk", f
			send(p)
		}
		if fail != nil {
			f := ev(step.StepFailed, "guest-image")
			f.Err = fail
			send(f)
			return fail
		}
		send(ev(step.StepDone, "guest-image"))
		return nil
	}
}

func TestPlainRun(t *testing.T) {
	var out bytes.Buffer
	elevated := 0
	err := RunPlain(context.Background(), PlainConfig{
		Out: &out, AssumeYes: true, Steps: setupSteps,
		Elevate: func(context.Context) error { elevated++; return nil },
		Run:     fakeEngine(errors.New("mkfs.ext4: \x1b]0;x\x07 no space left on device")),
	})
	if err == nil {
		t.Fatal("the run's failure got lost")
	}
	s := out.String()
	golden(t, "plain", s)
	if strings.ContainsRune(s, 0x1b) {
		t.Errorf("plain output holds escape codes: %q", s)
	}
	if elevated != 1 {
		t.Errorf("elevated %d times", elevated)
	}
	if strings.Count(s, "Building the guest root disk") != 3 {
		t.Errorf("progress should print at each new tenth (5%%, 42%%, 100%%):\n%s", s)
	}
}

// Without --yes and without anyone to ask, plain mode must refuse a plan,
// never approve it silently (a nil Confirm means yes to the engine).
func TestPlainRefusesWithoutYesOrAnswers(t *testing.T) {
	var out bytes.Buffer
	err := RunPlain(context.Background(), PlainConfig{Out: &out, Steps: setupSteps, Run: fakeEngine(nil)})
	if !errors.Is(err, ErrNeedsYes) {
		t.Errorf("err = %v, want ErrNeedsYes", err)
	}
	if !strings.Contains(out.String(), "--yes") {
		t.Errorf("no word about --yes:\n%s", out.String())
	}
}

func TestPlainAsksOnItsInput(t *testing.T) {
	for in, wantErr := range map[string]error{"y\n": nil, "yes\n": nil, "\n": step.ErrDeclined, "n\n": step.ErrDeclined, "": step.ErrDeclined} {
		var out bytes.Buffer
		err := RunPlain(context.Background(), PlainConfig{
			Out: &out, In: strings.NewReader(in), Steps: setupSteps, Run: fakeEngine(nil),
		})
		if !errors.Is(err, wantErr) && !(wantErr == nil && err == nil) {
			t.Errorf("answer %q: err = %v, want %v", in, err, wantErr)
		}
		if !strings.Contains(out.String(), "pacman -S --needed --noconfirm e2fsprogs") || !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("answer %q: the plan or question didn't print:\n%s", in, out.String())
		}
	}
}

func TestPlainShowsRootCommandsWithAHash(t *testing.T) {
	got := planLines(step.Plan{Cmds: []step.PlannedCmd{
		{Name: "ip", Args: []string{"link"}, Root: true},
		{Name: "git", Args: []string{"status"}},
	}, Files: []step.PlannedFile{{Path: "/etc/x", Mode: 0o600, Content: []byte("abc")}}})
	want := []string{"  # ip link", "  $ git status", "  write /etc/x (0600, 3 bytes)"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("planLines = %q", got)
	}
}
