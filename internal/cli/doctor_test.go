package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/setup"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// checkStep only checks.
type checkStep struct {
	id, title string
	status    step.Status
	err       error
	calls     *int
}

func (c checkStep) ID() string        { return c.id }
func (c checkStep) Title() string     { return c.title }
func (c checkStep) Phase() step.Phase { return step.PhaseHost }
func (c checkStep) Check(context.Context) (step.Status, error) {
	if c.calls != nil {
		*c.calls++
	}
	return c.status, c.err
}
func (checkStep) Plan(context.Context) (step.Plan, error) { return step.Plan{}, nil }
func (checkStep) Apply(context.Context) error             { return nil }
func (checkStep) Undo(context.Context, []step.Item) error { return nil }

func doctorEnv(steps ...step.Step) Env {
	env := testEnv()
	env.Setup = func(Env, Options) (*SetupHost, error) {
		return &SetupHost{Steps: func(setup.Deps) ([]step.Step, error) { return steps, nil }}, nil
	}
	return env
}

func TestDoctorOnAHealthyHost(t *testing.T) {
	out, err := runIn(t, doctorEnv(
		checkStep{id: "platform-kvm", title: "KVM access", status: step.Status{OK: true, Detail: "/dev/kvm usable"}},
		checkStep{id: "host-rules", title: "Firewall rules", status: step.Status{OK: true, Detail: "rules match"}},
	), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	for _, want := range []string{"KVM access", "/dev/kvm usable", "Firewall rules", "2 passed, 0 failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestDoctorOnADriftedHostExitsOne(t *testing.T) {
	out, err := runIn(t, doctorEnv(
		checkStep{id: "platform-kvm", title: "KVM access", status: step.Status{OK: true}},
		checkStep{id: "host-rules", title: "Firewall rules", status: step.Status{Detail: "the installed rules differ from what setup renders"}},
		checkStep{id: "guest-image", title: "Guest image", err: errors.New("reading build.json: permission denied")},
	), "doctor")
	if !errors.Is(err, ErrDoctorFailed) {
		t.Errorf("err = %v, want ErrDoctorFailed", err)
	}
	for _, want := range []string{"rules differ", "permission denied", "1 passed, 2 failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

// A check that needs root after the login lapsed gets one handover, then a
// retry.
func TestDoctorAsksForTheLoginOnce(t *testing.T) {
	calls := 0
	lapsed := checkStep{id: "host-ufw", title: "ufw", calls: &calls, err: fmt.Errorf("ufw: %w", system.ErrLoginExpired)}
	env := doctorEnv(lapsed)
	logins := 0
	env.Setup = func(Env, Options) (*SetupHost, error) {
		return &SetupHost{
			Steps:   func(setup.Deps) ([]step.Step, error) { return []step.Step{lapsed}, nil },
			Elevate: func(context.Context) error { logins++; return nil },
		}, nil
	}
	runIn(t, env, "doctor")
	if logins != 1 || calls != 2 {
		t.Errorf("logins %d, checks %d; want one login and a retry", logins, calls)
	}
}
