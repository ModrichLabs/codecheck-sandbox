package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/review"
	"github.com/ModrichLabs/codecheck-sandbox/internal/setup"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// scriptStep plays one setup step.
type scriptStep struct {
	id      string
	phase   step.Phase
	ok      bool // Check passes
	plan    step.Plan
	item    *step.Item // recorded on Apply
	fail    error      // Apply's error
	applied *[]string
	record  *func(step.Item)
}

func (s scriptStep) ID() string        { return s.id }
func (s scriptStep) Title() string     { return "Step " + s.id }
func (s scriptStep) Phase() step.Phase { return s.phase }
func (s scriptStep) Check(context.Context) (step.Status, error) {
	if s.ok || slices.Contains(*s.applied, s.id) {
		return step.Status{OK: true}, nil
	}
	return step.Status{}, nil
}
func (s scriptStep) Plan(context.Context) (step.Plan, error) { return s.plan, nil }
func (s scriptStep) Apply(context.Context) error {
	if s.fail != nil {
		return s.fail
	}
	*s.applied = append(*s.applied, s.id)
	if s.item != nil && *s.record != nil {
		(*s.record)(*s.item)
	}
	return nil
}
func (s scriptStep) Undo(context.Context, []step.Item) error { return nil }

// fakeSetup stands in for the host: scripted steps, an in-memory manifest
// and a lock.
type fakeSetup struct {
	mu       sync.Mutex
	steps    []scriptStep
	applied  []string
	record   func(step.Item)
	saved    *step.Manifest
	deps     setup.Deps
	locked   bool
	unlocked bool
}

func (f *fakeSetup) host(Env, Options) (*SetupHost, error) {
	return &SetupHost{
		Steps: func(d setup.Deps) ([]step.Step, error) {
			f.deps = d
			f.record = d.Record
			out := make([]step.Step, len(f.steps))
			for i, s := range f.steps {
				s.applied, s.record = &f.applied, &f.record
				out[i] = s
			}
			return out, nil
		},
		LoadManifest: func() (*step.Manifest, error) { return nil, nil },
		SaveManifest: func(_ context.Context, m *step.Manifest) error { f.saved = m; return nil },
		Lock: func(context.Context) (func(), error) {
			f.locked = true
			return func() { f.unlocked = true }, nil
		},
	}, nil
}

var installCmd = system.Cmd{Name: "pacman", Args: []string{"-S", "--needed", "--noconfirm", "nftables"}, Root: true}

func setupEnv(f *fakeSetup, sb *fakeSandbox) Env {
	env := testEnv()
	env.Setup = f.host
	env.Sandbox = func(Env, Options) (Sandbox, error) { return sb, nil }
	env.Environ["CODECHECK_API_KEY"] = "sk-ant-api03-test"
	return env
}

func passingSelftest() *fakeSandbox {
	return &fakeSandbox{result: review.RunResult{ReportDir: "/r", Summary: review.Summary{Outcome: review.OutcomeOK, Probes: &review.Probes{Passed: 9}}}}
}

func TestSetupDryRunPrintsPlansAndChangesNothing(t *testing.T) {
	f := &fakeSetup{steps: []scriptStep{
		{id: "platform-kvm", phase: step.PhasePlatform, ok: true},
		{id: "components", phase: step.PhaseComponents, plan: step.Plan{NeedsRoot: true, Cmds: []system.Cmd{installCmd}}},
		{id: "host-rules", phase: step.PhaseHost, plan: step.Plan{NeedsRoot: true, Files: []step.PlannedFile{{Path: "/etc/codecheck-sandbox/rules.nft", Mode: 0o644, Content: []byte("x")}}}},
	}}
	sb := passingSelftest()
	env := setupEnv(f, sb)
	delete(env.Environ, "CODECHECK_API_KEY") // a dry run needs no key
	out, err := runIn(t, env, "setup", "--dry-run")
	if err != nil {
		t.Fatalf("setup --dry-run: %v\n%s", err, out)
	}
	for _, want := range []string{"pacman -S --needed --noconfirm nftables", "write /etc/codecheck-sandbox/rules.nft"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run didn't print %q:\n%s", want, out)
		}
	}
	if len(f.applied) != 0 || f.saved != nil || len(sb.selftests) != 0 {
		t.Errorf("the dry run changed things: applied %v, manifest %v, selftests %d", f.applied, f.saved, len(sb.selftests))
	}
	if _, found, _ := env.LoadConfig(); found {
		t.Error("the dry run wrote config.toml")
	}
}

func TestSetupYesNeedsTheKeyUpFront(t *testing.T) {
	f := &fakeSetup{steps: []scriptStep{{id: "components", phase: step.PhaseComponents}}}
	env := setupEnv(f, passingSelftest())
	delete(env.Environ, "CODECHECK_API_KEY")
	_, err := runIn(t, env, "setup", "--yes")
	if err == nil || !strings.Contains(err.Error(), "CODECHECK_API_KEY") {
		t.Errorf("err = %v", err)
	}
	if len(f.applied) != 0 || f.locked {
		t.Error("setup started without the key")
	}
}

func TestSetupRunsThenVerifies(t *testing.T) {
	item := step.Item{ID: "components", Kind: step.KindPackage, Name: "nftables", State: step.Created}
	f := &fakeSetup{steps: []scriptStep{
		{id: "components", phase: step.PhaseComponents, plan: step.Plan{NeedsRoot: true, Cmds: []system.Cmd{installCmd}}, item: &item},
		{id: "host-rules", phase: step.PhaseHost},
	}}
	sb := passingSelftest()
	env := setupEnv(f, sb)
	out, err := runIn(t, env, "setup", "--yes", "--budget", "10")
	if err != nil {
		t.Fatalf("setup --yes: %v\n%s", err, out)
	}
	if !slices.Equal(f.applied, []string{"components", "host-rules"}) {
		t.Errorf("applied %v", f.applied)
	}
	if f.saved == nil || len(f.saved.Items) != 1 || f.saved.Items[0] != item || f.saved.Version != step.ManifestVersion {
		t.Errorf("manifest = %+v", f.saved)
	}
	if !f.locked || !f.unlocked {
		t.Error("setup ran without the lock")
	}
	if len(sb.selftests) != 1 || !strings.Contains(out, "9 passed") {
		t.Errorf("no selftest after setup:\n%s", out)
	}
	key, err := f.deps.APIKey()
	if err != nil || key != "sk-ant-api03-test" {
		t.Errorf("the key step would get %q, %v", key, err)
	}
	c, found, _ := env.LoadConfig()
	if !found || c.BudgetUSD != "10" {
		t.Errorf("config = %+v, found %v", c, found)
	}
	// --yes without an explicit telemetry setting leaves it off and unset.
	if c.Telemetry != nil {
		t.Errorf("setup --yes set telemetry to %v", *c.Telemetry)
	}
}

// What got created before a failure still reaches the manifest, so
// uninstall can remove it.
func TestSetupSavesTheManifestEvenWhenAStepFails(t *testing.T) {
	item := step.Item{ID: "components", Kind: step.KindPackage, Name: "git", State: step.Created}
	f := &fakeSetup{steps: []scriptStep{
		{id: "components", phase: step.PhaseComponents, item: &item},
		{id: "host-rules", phase: step.PhaseHost, fail: errors.New("nft: syntax error")},
	}}
	sb := passingSelftest()
	_, err := runIn(t, setupEnv(f, sb), "setup", "--yes")
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("err = %v", err)
	}
	if f.saved == nil || len(f.saved.Items) != 1 {
		t.Errorf("manifest = %+v", f.saved)
	}
	if len(sb.selftests) != 0 {
		t.Error("a failed setup still ran the selftest")
	}
}

func TestSetupMergesTheExistingManifest(t *testing.T) {
	old := step.Item{ID: "host-jail-user", Kind: step.KindUser, Name: "fcjail", State: step.Created}
	item := step.Item{ID: "components", Kind: step.KindPackage, Name: "git", State: step.Created}
	f := &fakeSetup{steps: []scriptStep{{id: "components", phase: step.PhaseComponents, item: &item}}}
	env := setupEnv(f, passingSelftest())
	env.Setup = func(e Env, o Options) (*SetupHost, error) {
		h, _ := f.host(e, o)
		h.LoadManifest = func() (*step.Manifest, error) {
			return &step.Manifest{Version: step.ManifestVersion, Items: []step.Item{old, item}}, nil
		}
		return h, nil
	}
	if _, err := runIn(t, env, "setup", "--yes"); err != nil {
		t.Fatal(err)
	}
	if f.saved == nil || !slices.Equal(f.saved.Items, []step.Item{old, item}) {
		t.Errorf("merged manifest = %+v", f.saved)
	}
}

func TestSetupRefusesRoot(t *testing.T) {
	f := &fakeSetup{}
	env := setupEnv(f, passingSelftest())
	env.EUID = 0
	env.Environ["USER"] = "root"
	if _, err := runIn(t, env, "setup", "--yes"); err == nil || !strings.Contains(err.Error(), "normal user") {
		t.Errorf("err = %v", err)
	}
}

func TestSetupKernelFlagNeedsAFile(t *testing.T) {
	f := &fakeSetup{steps: []scriptStep{{id: "components", phase: step.PhaseComponents, ok: true}}}
	if _, err := runIn(t, setupEnv(f, passingSelftest()), "setup", "--yes", "--kernel", "relative/vmlinux"); err == nil {
		t.Error("a relative --kernel passed")
	}
	if _, err := runIn(t, setupEnv(f, passingSelftest()), "setup", "--yes", "--kernel", "/nonexistent/vmlinux"); err == nil {
		t.Error("a missing --kernel passed")
	}
}
