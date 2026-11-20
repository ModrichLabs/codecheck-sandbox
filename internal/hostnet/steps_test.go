package hostnet

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

type rig struct {
	fs      *system.MemFS
	run     *system.FakeRunner
	records []step.Item
	apiKey  string
}

func newRig() *rig { return &rig{fs: system.NewMemFS(), run: &system.FakeRunner{}} }

func (r *rig) deps() Deps {
	return Deps{
		Config: DefaultConfig(), Runner: r.run, FS: r.fs,
		Record:     func(it step.Item) { r.records = append(r.records, it) },
		APIKey:     func() (string, error) { return r.apiKey, nil },
		Executable: func() (string, error) { return "/home/u/bin/codecheck-sandbox", nil },
	}
}

func (r *rig) step(t *testing.T, id string) step.Step {
	t.Helper()
	steps, err := Steps(r.deps())
	if err != nil {
		t.Fatalf("Steps: %v", err)
	}
	for _, s := range steps {
		if s.ID() == id {
			return s
		}
	}
	t.Fatalf("no step %q", id)
	return nil
}

func (r *rig) write(t *testing.T, path, content string, mode uint32) {
	t.Helper()
	_ = r.fs.MkdirAll(path[:strings.LastIndexByte(path, '/')], 0o755)
	if err := r.fs.WriteFile(path, []byte(content), fs.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
}

// argvs renders the recorded calls, marking root ones.
func (r *rig) argvs() []string {
	var out []string
	for _, c := range r.run.Calls() {
		s := strings.Join(append([]string{c.Name}, c.Args...), " ")
		if c.Root {
			s = "# " + s
		}
		out = append(out, s)
	}
	return out
}

func ok(code int) system.Result { return system.Result{Code: code} }

func rendered(t *testing.T, suffix string) string {
	t.Helper()
	files, _ := Render(DefaultConfig())
	for _, f := range files {
		if strings.HasSuffix(f.Path, suffix) {
			return string(f.Content)
		}
	}
	t.Fatalf("no rendered %s", suffix)
	return ""
}

func check(t *testing.T, s step.Step) step.Status {
	t.Helper()
	st, err := s.Check(context.Background())
	if err != nil {
		t.Fatalf("%s.Check: %v", s.ID(), err)
	}
	return st
}

func mustApply(t *testing.T, s step.Step) {
	t.Helper()
	if err := s.Apply(context.Background()); err != nil {
		t.Fatalf("%s.Apply: %v", s.ID(), err)
	}
}

func wantArgvs(t *testing.T, r *rig, want ...string) {
	t.Helper()
	if got := r.argvs(); !slices.Equal(got, want) {
		t.Errorf("commands =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestStepsOrderPhaseAndTelemetry(t *testing.T) {
	want := []string{"host-jail-user", "host-jailer-base", "host-rules", "host-nm-unmanaged",
		"host-tap-service", "host-firewalld", "host-ufw",
		"host-proxy-binary", "host-proxy-ca", "host-api-key", "host-proxy-service", "host-selinux"}
	var got []string
	steps, _ := Steps(newRig().deps())
	for _, s := range steps {
		got = append(got, s.ID())
		if s.Phase() != step.PhaseHost {
			t.Errorf("%s: phase %s", s.ID(), s.Phase())
		}
		props, _ := telemetry.Properties(telemetry.StepFinished{Step: telemetry.StepID(s.ID())})
		if props["step"] != s.ID() {
			t.Errorf("%s not registered with telemetry", s.ID())
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestStepsRefuseAnInvalidConfig(t *testing.T) {
	d := newRig().deps()
	d.Config.Tap = "eth0"
	if _, err := Steps(d); err == nil {
		t.Error("Steps accepted a config that renders nothing safe")
	}
}

func TestJailUserStep(t *testing.T) {
	r := newRig()
	r.run.On("getent", "passwd", "fcjail").Return(ok(2))
	s := r.step(t, "host-jail-user")
	if check(t, s).OK {
		t.Fatal("passed without the user")
	}
	plan, _ := s.Plan(context.Background())
	if !plan.NeedsRoot {
		t.Error("plan needs root")
	}
	r.run.On("useradd").Return(ok(0))
	mustApply(t, s)
	wantArgvs(t, r,
		"getent passwd fcjail",
		"# useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /bin/false fcjail")
	if !reflect.DeepEqual(r.records, []step.Item{{ID: "host-jail-user", Kind: step.KindUser, Name: "fcjail", State: step.Created}}) {
		t.Errorf("records = %+v", r.records)
	}

	r = newRig()
	r.run.On("userdel").Return(ok(0))
	if err := r.step(t, "host-jail-user").Undo(context.Background(), []step.Item{{ID: "host-jail-user", Kind: step.KindUser, Name: "fcjail", State: step.Created}}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	wantArgvs(t, r, "# userdel fcjail")
}

func TestJailerBaseStep(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-jailer-base")
	if check(t, s).OK {
		t.Fatal("passed without /srv/jailer")
	}
	r.run.On("install").Return(ok(0))
	mustApply(t, s)
	wantArgvs(t, r, "# install -d -m 0711 -o root -g root /srv/jailer")
	if len(r.records) != 1 || r.records[0].Path != "/srv/jailer" || r.records[0].Kind != step.KindDir {
		t.Errorf("records = %+v", r.records)
	}

	// An existing folder with the wrong mode gets fixed but never recorded,
	// so uninstall leaves it.
	r = newRig()
	_ = r.fs.MkdirAll("/srv/jailer", 0o755)
	s = r.step(t, "host-jailer-base")
	if check(t, s).OK {
		t.Fatal("mode 0755 passed")
	}
	r.run.On("install").Return(ok(0))
	mustApply(t, s)
	if len(r.records) != 0 {
		t.Errorf("recorded an adopted folder: %+v", r.records)
	}

	r = newRig()
	_ = r.fs.MkdirAll("/srv", 0o755)
	_ = r.fs.MkdirAll("/srv/jailer", 0o711)
	if !check(t, r.step(t, "host-jailer-base")).OK {
		t.Error("a correct /srv/jailer failed")
	}
	r.run.On("rm").Return(ok(0))
	_ = r.step(t, "host-jailer-base").Undo(context.Background(), []step.Item{{Kind: step.KindDir, Path: "/srv/jailer"}})
	wantArgvs(t, r, "# rm -rf --one-file-system -- /srv/jailer")
}

func TestRulesStepCheck(t *testing.T) {
	live, _ := HashRules(fixture(t))
	healthy := func() *rig {
		r := newRig()
		r.write(t, "/etc/codecheck-sandbox/rules.nft", rendered(t, "rules.nft"), 0o644)
		r.write(t, "/var/lib/codecheck-sandbox/rules.sha256", live+"\n", 0o644)
		return r
	}

	r := healthy()
	r.run.On("nft", "-j", "list").Return(system.Result{Stdout: fixture(t)})
	if st := check(t, r.step(t, "host-rules")); !st.OK {
		t.Errorf("healthy rules failed: %s", st.Detail)
	}

	r = healthy()
	r.write(t, "/var/lib/codecheck-sandbox/rules.sha256", strings.Repeat("0", 64)+"\n", 0o644)
	r.run.On("nft", "-j", "list").Return(system.Result{Stdout: fixture(t)})
	if check(t, r.step(t, "host-rules")).OK {
		t.Error("a live table that differs from the recorded hash passed")
	}

	r = healthy()
	r.run.On("nft", "-j", "list").Return(system.Result{Code: 1, Stderr: []byte("Error: No such file or directory")})
	if check(t, r.step(t, "host-rules")).OK {
		t.Error("a missing table passed")
	}

	r = healthy()
	r.run.On("nft", "-j", "list").Fail(fmt.Errorf("nft: %w", system.ErrLoginExpired))
	if _, err := r.step(t, "host-rules").Check(context.Background()); !errors.Is(err, system.ErrLoginExpired) {
		t.Errorf("err = %v, want ErrLoginExpired passed up so the engine hands over", err)
	}
}

func TestRulesStepApplyRecordsTheLiveHash(t *testing.T) {
	r := newRig()
	live, _ := HashRules(fixture(t))
	r.run.On("install", "-d").Return(ok(0))
	r.run.On("install", "-d").Return(ok(0))
	r.run.On("install", "-m").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("nft", "-f").Return(ok(0))
	r.run.On("nft", "-j", "list").Return(system.Result{Stdout: fixture(t)})
	r.run.On("install", "-m").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	mustApply(t, r.step(t, "host-rules"))
	wantArgvs(t, r,
		"# install -d -m 0755 /etc/codecheck-sandbox",
		"# install -d -m 0755 /var/lib/codecheck-sandbox",
		"# install -m 0644 -T /dev/null /etc/codecheck-sandbox/rules.nft",
		"# dd of=/etc/codecheck-sandbox/rules.nft status=none",
		"# nft -f /etc/codecheck-sandbox/rules.nft",
		"# nft -j list table inet codecheck",
		"# install -m 0644 -T /dev/null /var/lib/codecheck-sandbox/rules.sha256",
		"# dd of=/var/lib/codecheck-sandbox/rules.sha256 status=none")
	calls := r.run.Calls()
	if string(calls[3].Stdin) != rendered(t, "rules.nft") {
		t.Error("the rules file differs from the rendered template")
	}
	if string(calls[7].Stdin) != live+"\n" {
		t.Errorf("hash file = %q, want the live table's hash", calls[7].Stdin)
	}
	var names []string
	for _, it := range r.records {
		names = append(names, it.Path+it.Name)
	}
	want := []string{"/etc/codecheck-sandbox", "/var/lib/codecheck-sandbox", "/etc/codecheck-sandbox/rules.nft", "inet codecheck", "/var/lib/codecheck-sandbox/rules.sha256"}
	if !slices.Equal(names, want) {
		t.Errorf("records = %v, want %v", names, want)
	}
	for _, it := range r.records {
		if _, err := (&step.Manifest{Version: 1, Items: []step.Item{it}}).Marshal(); err != nil {
			t.Errorf("the manifest refuses record %+v: %v", it, err)
		}
	}
}

func TestRulesStepUndo(t *testing.T) {
	r := newRig()
	r.run.On("nft", "delete").Return(ok(0))
	r.run.On("rm").Return(ok(0))
	r.run.On("rm").Return(ok(0))
	r.run.On("rmdir").Return(ok(0))
	r.run.On("rmdir").Return(ok(0))
	items := []step.Item{
		{Kind: step.KindDir, Path: "/etc/codecheck-sandbox"},
		{Kind: step.KindDir, Path: "/var/lib/codecheck-sandbox"},
		{Kind: step.KindFile, Path: "/etc/codecheck-sandbox/rules.nft"},
		{Kind: step.KindNetwork, Name: "inet codecheck"},
		{Kind: step.KindFile, Path: "/var/lib/codecheck-sandbox/rules.sha256"},
	}
	if err := r.step(t, "host-rules").Undo(context.Background(), items); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	wantArgvs(t, r,
		"# nft delete table inet codecheck",
		"# rm -f -- /etc/codecheck-sandbox/rules.nft",
		"# rm -f -- /var/lib/codecheck-sandbox/rules.sha256",
		"# rmdir --ignore-fail-on-non-empty -- /var/lib/codecheck-sandbox",
		"# rmdir --ignore-fail-on-non-empty -- /etc/codecheck-sandbox")
}

func TestNMStepSkipsWithoutNetworkManager(t *testing.T) {
	r := newRig()
	r.run.On("systemctl", "is-active").Return(ok(3))
	if st := check(t, r.step(t, "host-nm-unmanaged")); !st.OK || !strings.Contains(st.Detail, "not running") {
		t.Errorf("status = %+v, want OK because NetworkManager doesn't run", st)
	}
}

func TestNMStepApply(t *testing.T) {
	r := newRig()
	r.run.On("systemctl", "is-active").Return(ok(0))
	s := r.step(t, "host-nm-unmanaged")
	if check(t, s).OK {
		t.Fatal("passed without the config")
	}
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("systemctl", "reload").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()
	if got[len(got)-1] != "# systemctl reload NetworkManager" {
		t.Errorf("last command = %s, want the reload", got[len(got)-1])
	}
	if len(r.records) != 1 || r.records[0].Path != "/etc/NetworkManager/conf.d/codecheck-sandbox.conf" {
		t.Errorf("records = %+v", r.records)
	}
}

func TestTapServiceStep(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-tap-service")
	if check(t, s).OK {
		t.Fatal("passed without the unit")
	}
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("systemctl").Return(ok(0))
	r.run.On("systemctl").Return(ok(0))
	r.run.On("systemctl").Return(ok(0))
	mustApply(t, s)
	wantArgvs(t, r,
		"# install -m 0644 -T /dev/null /etc/systemd/system/codecheck-net.service",
		"# dd of=/etc/systemd/system/codecheck-net.service status=none",
		"# systemctl daemon-reload",
		"# systemctl enable codecheck-net.service",
		"# systemctl restart codecheck-net.service")

	r = newRig()
	r.write(t, "/etc/systemd/system/codecheck-net.service", rendered(t, ".service"), 0o644)
	r.run.On("systemctl", "is-enabled").Return(ok(0))
	r.run.On("systemctl", "is-active").Return(ok(0))
	r.run.On("ip", "link", "show", "dev", "cc-tap0").Return(ok(0))
	if !check(t, r.step(t, "host-tap-service")).OK {
		t.Error("an enabled, active, current unit failed")
	}

	// A oneshot unit stays "active" after someone deletes the tap.
	r = newRig()
	r.write(t, "/etc/systemd/system/codecheck-net.service", rendered(t, ".service"), 0o644)
	r.run.On("systemctl", "is-enabled").Return(ok(0))
	r.run.On("systemctl", "is-active").Return(ok(0))
	r.run.On("ip", "link", "show").Return(ok(1))
	if check(t, r.step(t, "host-tap-service")).OK {
		t.Error("passed with the tap gone")
	}
}

func TestUnitToleratesAHostWithoutIPv6(t *testing.T) {
	if !strings.Contains(rendered(t, ".service"), "ExecStart=-sysctl -q -w net.ipv6.conf.cc-tap0.disable_ipv6=1") {
		t.Error("the IPv6 sysctl must carry '-', or ipv6.disable=1 hosts fail the unit")
	}
}

func TestRulesUndoAfterTheTapServiceRemovedTheTable(t *testing.T) {
	r := newRig()
	r.run.On("nft", "delete").Return(system.Result{Code: 1, Stderr: []byte("Error: Could not process rule: No such file or directory\ndelete table inet codecheck\n")})
	err := r.step(t, "host-rules").Undo(context.Background(), []step.Item{{Kind: step.KindNetwork, Name: "inet codecheck"}})
	if err != nil {
		t.Errorf("Undo = %v; a table already gone counts as removed", err)
	}
}

func TestRecordsStayUniqueWhenApplyRunsTwice(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-jail-user")
	r.run.On("useradd").Return(ok(0))
	r.run.On("useradd").Return(ok(0))
	mustApply(t, s)
	mustApply(t, s) // the engine retries Apply after a login handover
	if len(r.records) != 1 {
		t.Errorf("records = %+v, want one", r.records)
	}
}

func TestFirewalldApplyChecksTheZoneQuery(t *testing.T) {
	r := newRig()
	r.run.On("firewall-cmd", "--permanent", "--get-zones").Return(system.Result{Code: 252, Stderr: []byte("FirewallD is not running")})
	if err := r.step(t, "host-firewalld").Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("Apply = %v, want the failed zone query", err)
	}
}

func TestFirewalldPlanSkipsAnExistingZone(t *testing.T) {
	r := newRig()
	r.run.On("firewall-cmd", "--permanent", "--get-zones").Return(system.Result{Stdout: []byte("codecheck public\n")})
	plan, _ := r.step(t, "host-firewalld").Plan(context.Background())
	for _, c := range plan.Cmds {
		if slices.Contains(c.Args, "--new-zone=codecheck") {
			t.Errorf("plan lists --new-zone although the zone exists: %+v", plan.Cmds)
		}
	}
	if c := r.run.Calls()[0]; c.Root {
		t.Error("the plan's zone query ran as root; the engine elevates only after planning")
	}
}

func TestFirewalldUndoWithFirewalldStopped(t *testing.T) {
	r := newRig()
	r.run.On("firewall-cmd", "--permanent", "--delete-zone=codecheck").Return(ok(0))
	r.run.On("systemctl", "is-active").Return(ok(3))
	err := r.step(t, "host-firewalld").Undo(context.Background(), []step.Item{{Kind: step.KindNetwork, Name: "firewalld zone codecheck"}})
	if err != nil {
		t.Errorf("Undo = %v; with firewalld stopped, skip the reload", err)
	}
}

func TestChecksReportFailedCommands(t *testing.T) {
	r := newRig()
	r.write(t, "/sys/fs/selinux/enforce", "1", 0o644)
	_ = r.fs.MkdirAll("/srv/jailer", 0o711)
	r.run.On("restorecon").Return(system.Result{Code: 255, Stderr: []byte("restorecon: lstat failed")})
	if st, err := r.step(t, "host-selinux").Check(context.Background()); err == nil && st.OK {
		t.Error("a failed restorecon passed as 'labels match'")
	}

	r = newRig()
	r.write(t, "/etc/ufw/ufw.conf", "ENABLED=yes\n", 0o644)
	r.run.On("ufw").Return(system.Result{Code: 1, Stderr: []byte("ERROR: problem running ufw-init")})
	if _, err := r.step(t, "host-ufw").Check(context.Background()); err == nil {
		t.Error("a failed ufw show added passed silently")
	}
}

func TestConfigRefusesNetworkAndBroadcastAddresses(t *testing.T) {
	for _, guest := range []string{"172.16.0.0", "172.16.0.3"} {
		c := DefaultConfig()
		c.GuestIP = guest
		if c.Validate() == nil {
			t.Errorf("guest %s accepted in 172.16.0.0/30", guest)
		}
	}
	c := DefaultConfig()
	c.HostIP, c.GuestIP = "172.16.0.0", "172.16.0.1"
	if c.Validate() == nil {
		t.Error("host on the network address accepted")
	}
}

func TestFirewalldStep(t *testing.T) {
	r := newRig()
	r.run.On("systemctl", "is-active").Return(ok(3))
	if !check(t, r.step(t, "host-firewalld")).OK {
		t.Error("failed without firewalld")
	}

	r = newRig()
	r.run.On("systemctl", "is-active").Return(ok(0))
	r.run.On("firewall-cmd", "--permanent", "--zone=codecheck", "--query-interface=cc-tap0").Return(ok(112))
	s := r.step(t, "host-firewalld")
	if check(t, s).OK {
		t.Fatal("passed without the zone")
	}
	r.run.On("firewall-cmd", "--permanent", "--get-zones").Return(system.Result{Stdout: []byte("block dmz public trusted\n")})
	r.run.On("firewall-cmd").Return(ok(0))
	r.run.On("firewall-cmd").Return(ok(0))
	r.run.On("firewall-cmd").Return(ok(0))
	r.run.On("firewall-cmd").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()[2:]
	want := []string{
		"# firewall-cmd --permanent --get-zones",
		"# firewall-cmd --permanent --new-zone=codecheck",
		"# firewall-cmd --permanent --zone=codecheck --change-interface=cc-tap0",
		"# firewall-cmd --permanent --zone=codecheck --add-port=3180/tcp",
		"# firewall-cmd --reload",
	}
	if !slices.Equal(got, want) {
		t.Errorf("commands = %v, want %v", got, want)
	}
	if len(r.records) != 1 || r.records[0].Name != "firewalld zone codecheck" {
		t.Errorf("records = %+v", r.records)
	}
}

func TestUfwStep(t *testing.T) {
	r := newRig()
	if !check(t, r.step(t, "host-ufw")).OK {
		t.Error("failed without ufw")
	}

	r = newRig()
	r.write(t, "/etc/ufw/ufw.conf", "ENABLED=yes\n", 0o644)
	r.run.On("ufw", "show", "added").Return(system.Result{Stdout: []byte("Added user rules:\nufw allow 22/tcp\n")})
	s := r.step(t, "host-ufw")
	if check(t, s).OK {
		t.Fatal("passed without the rule")
	}
	r.run.On("ufw", "allow").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()
	if got[1] != "# ufw allow in on cc-tap0 to 172.16.0.1 port 3180 proto tcp comment codecheck-sandbox" {
		t.Errorf("rule = %s", got[1])
	}

	r = newRig()
	r.write(t, "/etc/ufw/ufw.conf", "ENABLED=yes\n", 0o644)
	r.run.On("ufw", "show", "added").Return(system.Result{Stdout: []byte("ufw allow in on cc-tap0 to 172.16.0.1 port 3180 proto tcp comment 'codecheck-sandbox'\n")})
	if !check(t, r.step(t, "host-ufw")).OK {
		t.Error("an existing rule failed")
	}
	r.run.On("ufw", "delete").Return(ok(0))
	_ = r.step(t, "host-ufw").Undo(context.Background(), []step.Item{{Kind: step.KindNetwork, Name: "ufw allow cc-tap0 3180"}})
	if got := r.argvs(); got[len(got)-1] != "# ufw delete allow in on cc-tap0 to 172.16.0.1 port 3180 proto tcp" {
		t.Errorf("undo = %s", got[len(got)-1])
	}
}

func TestSELinuxStep(t *testing.T) {
	r := newRig()
	if !check(t, r.step(t, "host-selinux")).OK {
		t.Error("failed without SELinux")
	}

	r = newRig()
	r.write(t, "/sys/fs/selinux/enforce", "1", 0o644)
	_ = r.fs.MkdirAll("/etc/codecheck-sandbox", 0o755)
	_ = r.fs.MkdirAll("/srv/jailer", 0o711)
	r.run.On("restorecon", "-RnvF").Return(system.Result{Stdout: []byte("Would relabel /srv/jailer from var_t to default_t\n")})
	s := r.step(t, "host-selinux")
	if check(t, s).OK {
		t.Fatal("passed with labels to fix")
	}
	r.run.On("restorecon", "-RF").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()
	if got[1] != "# restorecon -RF -- /etc/codecheck-sandbox /srv/jailer" {
		t.Errorf("relabel = %s; want only the paths that exist", got[1])
	}
}
