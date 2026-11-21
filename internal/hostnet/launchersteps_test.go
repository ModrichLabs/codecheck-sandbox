package hostnet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestLauncherStep(t *testing.T) {
	r := newRig()
	r.write(t, "/home/u/bin/cc-launch", "launcher v2", 0o755)
	r.write(t, "/usr/local/lib/codecheck-sandbox/bin/cc-launch", "launcher v1", 0o755)
	s := r.step(t, "host-launcher")
	if check(t, s).OK {
		t.Fatal("an outdated launcher passed")
	}
	r.run.On("install").Return(ok(0))
	mustApply(t, s)
	wantArgvs(t, r, "# install -D -m 0755 -o root -g root -T /home/u/bin/cc-launch /usr/local/lib/codecheck-sandbox/bin/cc-launch")

	r = newRig()
	r.write(t, "/home/u/bin/cc-launch", "launcher", 0o755)
	r.write(t, "/usr/local/lib/codecheck-sandbox/bin/cc-launch", "launcher", 0o755)
	if !check(t, r.step(t, "host-launcher")).OK {
		t.Error("an identical launcher failed")
	}
}

func TestLauncherStepNeedsTheShippedBinary(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-launcher")
	if err := s.Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "cc-launch") {
		t.Errorf("Apply = %v, want a missing cc-launch beside the CLI", err)
	}
	if len(r.run.Calls()) != 0 {
		t.Error("Apply ran commands without a launcher to install")
	}
}

const wantTmpfiles = "d /run/codecheck-sandbox 0755 root root -\nf /run/codecheck-sandbox/lock 0644 root root -\n"

func TestLockStep(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-lock")
	if check(t, s).OK {
		t.Fatal("passed without the tmpfiles entry")
	}
	plan, _ := s.Plan(context.Background())
	if len(plan.Files) != 1 || plan.Files[0].Path != "/etc/tmpfiles.d/codecheck-sandbox.conf" || !strings.HasSuffix(string(plan.Files[0].Content), wantTmpfiles) {
		t.Fatalf("plan files = %+v", plan.Files)
	}
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("systemd-tmpfiles").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()
	if got[len(got)-1] != "# systemd-tmpfiles --create /etc/tmpfiles.d/codecheck-sandbox.conf" {
		t.Errorf("commands = %v", got)
	}

	r = newRig()
	r.write(t, "/etc/tmpfiles.d/codecheck-sandbox.conf", string(plan.Files[0].Content), 0o644)
	if check(t, r.step(t, "host-lock")).OK {
		t.Error("passed without the lock file in /run")
	}
	r.write(t, "/run/codecheck-sandbox/lock", "", 0o644)
	if !check(t, r.step(t, "host-lock")).OK {
		t.Error("a current entry with its lock failed")
	}
}

func TestSudoersStep(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-sudoers")
	if check(t, s).OK {
		t.Fatal("passed without the drop-in")
	}
	plan, _ := s.Plan(context.Background())
	if len(plan.Files) != 1 || plan.Files[0].Mode != 0o440 ||
		!strings.HasSuffix(string(plan.Files[0].Content), "u ALL=(root) NOPASSWD: /usr/local/lib/codecheck-sandbox/bin/cc-launch\n") {
		t.Fatalf("plan files = %+v", plan.Files)
	}
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("visudo").Return(ok(0))
	r.run.On("mv").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()
	want := []string{
		"# visudo -cf /etc/sudoers.d/codecheck-sandbox.new",
		"# mv -f -T /etc/sudoers.d/codecheck-sandbox.new /etc/sudoers.d/codecheck-sandbox",
	}
	if !slices.Equal(got[2:], want) {
		t.Errorf("commands = %v, want the write, then %v", got, want)
	}
	// The draft carries a dot, so sudo ignores it even if it stays behind.
	if c := r.run.Calls()[0]; !strings.Contains(strings.Join(c.Args, " "), "/etc/sudoers.d/codecheck-sandbox.new") {
		t.Errorf("first write went to %v", c.Args)
	}

	r = newRig()
	r.write(t, "/etc/sudoers.d/codecheck-sandbox", string(plan.Files[0].Content), 0o440)
	if !check(t, r.step(t, "host-sudoers")).OK {
		t.Error("a current drop-in failed")
	}
}

func TestSudoersStepDropsADraftVisudoRejects(t *testing.T) {
	r := newRig()
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("visudo").Return(system.Result{Code: 1, Stderr: []byte("parse error")})
	r.run.On("rm").Return(ok(0))
	if err := r.step(t, "host-sudoers").Apply(context.Background()); err == nil {
		t.Fatal("Apply accepted a drop-in visudo rejected")
	}
	got := r.argvs()
	if got[len(got)-1] != "# rm -f -- /etc/sudoers.d/codecheck-sandbox.new" || slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "# mv") }) {
		t.Errorf("commands = %v; want the draft removed and nothing moved", got)
	}
}

func TestSudoersStepRefusesOddUserNames(t *testing.T) {
	for _, u := range []string{"", "root", "%wheel", "a b", "u\nALL ALL=(ALL) ALL", "ALL"} {
		r := newRig()
		d := r.deps()
		d.User = u
		steps, err := Steps(d)
		if err != nil {
			continue // refusing up front works too
		}
		for _, s := range steps {
			if s.ID() == "host-sudoers" {
				if err := s.Apply(context.Background()); err == nil {
					t.Errorf("user %q accepted", u)
				}
			}
		}
		if len(r.run.Calls()) != 0 {
			t.Errorf("user %q: wrote something", u)
		}
	}
}

// The real visudo accepts the rendered drop-in, when it exists here.
func TestSudoersDropInPassesVisudo(t *testing.T) {
	visudo := "/usr/bin/visudo"
	if _, err := os.Stat(visudo); err != nil {
		visudo = "/usr/sbin/visudo"
		if _, err := os.Stat(visudo); err != nil {
			t.Skip("no visudo on this machine")
		}
	}
	plan, _ := newRig().step(t, "host-sudoers").Plan(context.Background())
	p := filepath.Join(t.TempDir(), "codecheck-sandbox")
	if err := os.WriteFile(p, plan.Files[0].Content, 0o440); err != nil {
		t.Fatal(err)
	}
	res, err := system.ExecRunner{}.Run(context.Background(), system.Cmd{Name: visudo, Args: []string{"-cf", p}})
	if err != nil || res.Code != 0 {
		t.Errorf("visudo -cf: %v %s%s", err, res.Stdout, res.Stderr)
	}
}
