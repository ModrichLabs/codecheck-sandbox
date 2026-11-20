package platform

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

const gib = 1 << 30

// fixture builds a healthy x86_64 host; tests break one part at a time.
type fixture struct {
	fs    *system.MemFS
	stats map[string]FileStat
	free  uint64
	arch  string
	path  map[string]string
	run   *system.FakeRunner
}

func healthy(t *testing.T) *fixture {
	t.Helper()
	m := system.NewMemFS()
	write := func(p, s string) {
		t.Helper()
		if err := m.MkdirAll(dirOf(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := m.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("/proc/meminfo", "MemTotal:       32621644 kB\nMemFree:         1048576 kB\n")
	write("/sys/fs/cgroup/cgroup.controllers", "cpuset cpu io memory hugetlb pids rdma misc\n")
	write("/etc/group", "root:x:0:\nkvm:x:992:\nwheel:x:998:lucas\n")
	_ = m.MkdirAll("/run/systemd/system", 0o755)
	run := &system.FakeRunner{}
	run.On("/usr/bin/sudo", "--version").Return(system.Result{Stdout: []byte("Sudo version 1.9.17p2\n")})
	return &fixture{
		fs:    m,
		stats: map[string]FileStat{"/dev/kvm": {Mode: fs.ModeDevice | fs.ModeCharDevice | 0o660, GID: 992}},
		free:  50 * gib,
		arch:  "amd64",
		path:  map[string]string{"sudo": "/usr/bin/sudo"},
		run:   run,
	}
}

func dirOf(p string) string { return p[:strings.LastIndexByte(p, '/')] }

func (f *fixture) env() Env {
	return Env{
		FS:     f.fs,
		Runner: f.run,
		Arch:   f.arch,
		Stat: func(p string) (FileStat, error) {
			if s, ok := f.stats[p]; ok {
				return s, nil
			}
			return FileStat{}, fs.ErrNotExist
		},
		FreeBytes: func(string) (uint64, error) { return f.free, nil },
		LookPath: func(n string) (string, error) {
			if p, ok := f.path[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
	}
}

func find(t *testing.T, env Env, id string) step.Step {
	t.Helper()
	for _, s := range Checks(env) {
		if s.ID() == id {
			return s
		}
	}
	t.Fatalf("no check %q", id)
	return nil
}

func check(t *testing.T, env Env, id string) step.Status {
	t.Helper()
	st, err := find(t, env, id).Check(context.Background())
	if err != nil {
		t.Fatalf("%s.Check: %v", id, err)
	}
	return st
}

func TestHealthyHostPassesEveryCheck(t *testing.T) {
	env := healthy(t).env()
	for _, s := range Checks(env) {
		if s.Phase() != step.PhasePlatform {
			t.Errorf("%s: phase %s, want platform", s.ID(), s.Phase())
		}
		st, err := s.Check(context.Background())
		if err != nil || !st.OK {
			t.Errorf("%s: %+v, %v; want OK on a healthy host", s.ID(), st, err)
		}
	}
}

func TestArch(t *testing.T) {
	f := healthy(t)
	f.arch = "arm64"
	st := check(t, f.env(), "platform-arch")
	if st.OK || !strings.Contains(st.Detail, "deferred") {
		t.Errorf("arm64: %+v, want a failure that says support is deferred", st)
	}
	f.arch = "386"
	if check(t, f.env(), "platform-arch").OK {
		t.Error("386 passed")
	}
}

func TestKVM(t *testing.T) {
	tests := []struct {
		name string
		stat *FileStat
		ok   bool
	}{
		{"kvm group rw", &FileStat{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o660, GID: 992}, true},
		{"world rw", &FileStat{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o666, GID: 0}, true},
		{"root only", &FileStat{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o600, GID: 0}, false},
		{"group rw but not kvm", &FileStat{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o660, GID: 998}, false},
		{"not a device", &FileStat{Mode: 0o666}, false},
		{"missing", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := healthy(t)
			if tt.stat == nil {
				delete(f.stats, "/dev/kvm")
			} else {
				f.stats["/dev/kvm"] = *tt.stat
			}
			if st := check(t, f.env(), "platform-kvm"); st.OK != tt.ok {
				t.Errorf("OK = %v (%s), want %v", st.OK, st.Detail, tt.ok)
			}
		})
	}
}

func TestSystemdAndCgroups(t *testing.T) {
	f := healthy(t)
	_ = f.fs.Remove("/run/systemd/system")
	if check(t, f.env(), "platform-systemd").OK {
		t.Error("systemd passed without /run/systemd/system")
	}
	f = healthy(t)
	_ = f.fs.Remove("/sys/fs/cgroup/cgroup.controllers")
	if check(t, f.env(), "platform-cgroup2").OK {
		t.Error("cgroup2 passed without cgroup.controllers (a cgroup v1 host)")
	}
}

func TestRAM(t *testing.T) {
	tests := []struct {
		kb string
		ok bool
	}{
		{"32621644", true},
		{"7949136", true},  // an "8 GB" laptop: about 7.6 GiB after reservations
		{"7340032", false}, // 7 GiB
		{"4000000", false},
	}
	for _, tt := range tests {
		f := healthy(t)
		_ = f.fs.WriteFile("/proc/meminfo", []byte("MemTotal: "+tt.kb+" kB\n"), 0o644)
		if st := check(t, f.env(), "platform-ram"); st.OK != tt.ok {
			t.Errorf("MemTotal %s kB: OK = %v (%s), want %v", tt.kb, st.OK, st.Detail, tt.ok)
		}
	}
	f := healthy(t)
	_ = f.fs.WriteFile("/proc/meminfo", []byte("garbage\n"), 0o644)
	if _, err := find(t, f.env(), "platform-ram").Check(context.Background()); err == nil {
		t.Error("an unreadable meminfo passed silently")
	}
}

func TestFreeDisk(t *testing.T) {
	f := healthy(t)
	f.free = 9 * gib
	if check(t, f.env(), "platform-disk").OK {
		t.Error("9 GiB free passed")
	}
	f.free = 10 * gib
	if !check(t, f.env(), "platform-disk").OK {
		t.Error("10 GiB free failed")
	}
}

func TestPrivilegeTool(t *testing.T) {
	f := healthy(t)
	f.path = map[string]string{}
	st := check(t, f.env(), "platform-privilege")
	if st.OK {
		t.Error("passed with no privilege tool")
	}
}

func TestFirewallAndSecurityModuleReport(t *testing.T) {
	f := healthy(t)
	_ = f.fs.MkdirAll("/run/firewalld", 0o755)
	_ = f.fs.MkdirAll("/run", 0o755)
	_ = f.fs.WriteFile("/run/docker.sock", nil, 0o660)
	_ = f.fs.MkdirAll("/sys/fs/selinux", 0o755)
	_ = f.fs.WriteFile("/sys/fs/selinux/enforce", []byte("1"), 0o644)

	fw := check(t, f.env(), "platform-firewall")
	if !fw.OK || !strings.Contains(fw.Detail, "firewalld") || !strings.Contains(fw.Detail, "docker") {
		t.Errorf("firewall = %+v, want OK naming firewalld and docker", fw)
	}
	sm := check(t, f.env(), "platform-security-module")
	if !sm.OK || !strings.Contains(sm.Detail, "selinux") || !strings.Contains(sm.Detail, "enforcing") {
		t.Errorf("security module = %+v, want selinux enforcing", sm)
	}

	r := Inspect(f.env())
	if r.Firewall != telemetry.FirewallManager("several") || r.SecurityModule != "selinux" || r.SecurityMode != "enforcing" {
		t.Errorf("Inspect = %+v", r)
	}
}

func TestInspectQuietHost(t *testing.T) {
	f := healthy(t)
	_ = f.fs.MkdirAll("/etc/ufw", 0o755)
	_ = f.fs.WriteFile("/etc/ufw/ufw.conf", []byte("# comment\nENABLED=yes\nLOGLEVEL=low\n"), 0o644)
	_ = f.fs.MkdirAll("/sys/module/apparmor/parameters", 0o755)
	_ = f.fs.WriteFile("/sys/module/apparmor/parameters/enabled", []byte("Y\n"), 0o644)
	r := Inspect(f.env())
	if r.Firewall != "ufw" || r.SecurityModule != "apparmor" || r.SecurityMode != "enforcing" {
		t.Errorf("Inspect = %+v", r)
	}
	f = healthy(t)
	if r := Inspect(f.env()); r.Firewall != "none" || r.SecurityModule != "none" {
		t.Errorf("Inspect on a bare host = %+v", r)
	}
}

func TestFailingReadOnlyCheckExplainsTheFix(t *testing.T) {
	f := healthy(t)
	f.free = gib
	s := find(t, f.env(), "platform-disk")
	plan, err := s.Plan(context.Background())
	if err != nil || len(plan.Cmds) != 0 || len(plan.Files) != 0 || plan.NeedsRoot {
		t.Errorf("Plan = %+v, %v; a read-only check plans nothing", plan, err)
	}
	err = s.Apply(context.Background())
	if !errors.Is(err, ErrManual) || !strings.Contains(err.Error(), "fix manually") {
		t.Errorf("Apply = %v, want ErrManual with a hint", err)
	}
	if err := s.Undo(context.Background(), nil); err != nil {
		t.Errorf("Undo = %v, want nil", err)
	}
}

func TestCheckIDsGoOutInTelemetry(t *testing.T) {
	for _, s := range Checks(healthy(t).env()) {
		props, _ := telemetry.Properties(telemetry.StepFinished{Step: telemetry.StepID(s.ID())})
		if props["step"] != s.ID() {
			t.Errorf("%s encodes as %v; register it with telemetry", s.ID(), props["step"])
		}
	}
}

func TestDefaultEnvOnThisHost(t *testing.T) {
	env := DefaultEnv()
	st, err := env.Stat("/dev/null")
	if err != nil || st.Mode&fs.ModeCharDevice == 0 {
		t.Errorf("Stat(/dev/null) = %+v, %v; want a char device", st, err)
	}
	if free, err := env.FreeBytes("/"); err != nil || free == 0 {
		t.Errorf("FreeBytes(/) = %d, %v", free, err)
	}
}
