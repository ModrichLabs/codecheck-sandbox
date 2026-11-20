package guestinit

import (
	"context"
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestMountKernelFS(t *testing.T) {
	s := newFakeSys()
	if err := MountKernelFS(s); err != nil {
		t.Fatalf("MountKernelFS: %v", err)
	}
	for _, want := range []struct{ target, fstype string }{
		{"/proc", "proc"}, {"/sys", "sysfs"}, {"/dev", "devtmpfs"},
	} {
		m, ok := s.mountAt(want.target)
		if !ok || m.FSType != want.fstype {
			t.Errorf("%s: got %+v, want a %s mount", want.target, m, want.fstype)
			continue
		}
		if m.Flags&unix.MS_NOSUID == 0 {
			t.Errorf("%s mounted without nosuid", want.target)
		}
	}
}

func TestMountKernelFSToleratesAnAutoMountedDev(t *testing.T) {
	s := newFakeSys()
	s.mountErr["/dev"] = syscall.EBUSY // the kernel's devtmpfs automount got there first
	if err := MountKernelFS(s); err != nil {
		t.Errorf("MountKernelFS: %v", err)
	}
}

func TestReadConfig(t *testing.T) {
	s := newFakeSys()
	s.devices[ConfigDevice] = append(block("mode=build\nrun_id=b1\n"), make([]byte, 8192)...)
	c, err := ReadConfig(s)
	if err != nil || c.Mode != ModeBuild {
		t.Fatalf("ReadConfig = %+v, %v", c, err)
	}
}

func reviewConfig() Config {
	return Config{Mode: ModeReview, RunID: "r1", MaxBudgetUSD: "25", Scope: ScopeDiff, Base: strings.Repeat("a", 40)}
}

func scriptNetwork(f *system.FakeRunner) {
	f.On(ipPath, "link", "set", "lo", "up").Return(system.Result{})
	f.On(ipPath, "addr", "add", "172.16.0.2/30", "dev", "eth0").Return(system.Result{})
	f.On(ipPath, "link", "set", "eth0", "up").Return(system.Result{})
}

func TestRunJobReview(t *testing.T) {
	s := newFakeSys()
	f := &system.FakeRunner{}
	scriptNetwork(f)
	f.On(setprivPath).Return(system.Result{Code: 7})

	code, err := RunJob(context.Background(), Env{Sys: s, Run: f}, reviewConfig())
	if err != nil || code != 7 {
		t.Fatalf("RunJob = %d, %v; want 7, nil", code, err)
	}

	// Writable paths get a tmpfs; nothing writes to the read-only root.
	for _, p := range []string{"/tmp", "/run", "/var/tmp", "/home/agent"} {
		m, ok := s.mountAt(p)
		if !ok || m.FSType != "tmpfs" || m.Flags&(unix.MS_NOSUID|unix.MS_NODEV) != unix.MS_NOSUID|unix.MS_NODEV {
			t.Errorf("%s: got %+v, want a nosuid,nodev tmpfs", p, m)
		}
	}
	if m, _ := s.mountAt("/home/agent"); !strings.Contains(m.Data, "uid=10001") || !strings.Contains(m.Data, "mode=0700") {
		t.Errorf("/home/agent data = %q, want the agent's own 0700 folder", m.Data)
	}

	// The code disk: read-only, no journal replay, nothing runnable.
	code1, ok := s.mountAt("/work")
	wantRO := uintptr(unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if !ok || code1.Source != "/dev/vdc" || code1.FSType != "ext4" || code1.Flags&wantRO != wantRO || !strings.Contains(code1.Data, "noload") {
		t.Errorf("/work: got %+v, want /dev/vdc ext4 ro,nosuid,nodev,noexec,noload", code1)
	}
	out, ok := s.mountAt("/out")
	wantRW := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
	if !ok || out.Source != "/dev/vdd" || out.Flags&wantRW != wantRW || out.Flags&unix.MS_RDONLY != 0 {
		t.Errorf("/out: got %+v, want /dev/vdd writable,nosuid,nodev,noexec", out)
	}

	// IPv6 goes off before any link comes up.
	for _, p := range []string{"/proc/sys/net/ipv6/conf/all/disable_ipv6", "/proc/sys/net/ipv6/conf/default/disable_ipv6"} {
		if f, ok := s.files[p]; !ok || f.String() != "1\n" {
			t.Errorf("%s not set to 1", p)
		}
	}

	calls := f.Calls()
	if len(calls) != 4 {
		t.Fatalf("ran %d commands, want 4: %+v", len(calls), calls)
	}
	for _, c := range calls[:3] {
		if slices.Contains(c.Args, "route") || slices.Contains(c.Args, "default") {
			t.Errorf("network setup added a route: %v", c.Args)
		}
	}

	job := calls[3]
	wantArgs := []string{"--reuid=10001", "--regid=10001", "--clear-groups", "--no-new-privs",
		"--inh-caps=-all", "--bounding-set=-all", "--", "/usr/local/bin/cc-claude"}
	if !slices.Equal(job.Args, wantArgs) {
		t.Errorf("setpriv args = %v, want %v", job.Args, wantArgs)
	}
	if !job.CleanEnv || job.Root {
		t.Errorf("job: CleanEnv %v, Root %v; want a clean environment and no privilege tool", job.CleanEnv, job.Root)
	}
	for k, want := range map[string]string{
		"HOME": "/home/agent", "PATH": "/usr/local/bin:/usr/bin:/bin",
		"CC_RUN_ID": "r1", "CC_MAX_BUDGET_USD": "25", "CC_SCOPE": "diff", "CC_BASE": strings.Repeat("a", 40),
	} {
		if got, _ := envValue(job.Env, k); got != want {
			t.Errorf("job env %s = %q, want %q", k, got, want)
		}
	}
	if job.Dir != "/home/agent" {
		t.Errorf("job Dir = %q, want /home/agent", job.Dir)
	}
	if job.Stdout != s.files["/out/stream.jsonl"] || job.Stderr != s.files["/out/stderr.log"] {
		t.Error("job output doesn't go to /out/stream.jsonl and /out/stderr.log")
	}
	for _, p := range []string{"/out/stream.jsonl", "/out/stderr.log"} {
		if f := s.files[p]; f == nil || !f.closed || f.mode != 0o600 {
			t.Errorf("%s: want a closed 0600 file, got %+v", p, f)
		}
	}
	if got := s.files["/out/exit-code"]; got == nil || got.String() != "7\n" {
		t.Errorf("exit-code = %v, want 7", got)
	}
	if s.synced == 0 {
		t.Error("RunJob never synced the output disk")
	}
}

func TestRunJobSelftest(t *testing.T) {
	s := newFakeSys()
	f := &system.FakeRunner{}
	scriptNetwork(f)
	f.On(setprivPath).Return(system.Result{})

	cfg := Config{Mode: ModeSelftest, RunID: "s1", ProbePorts: []int{8080, 5432}}
	if code, err := RunJob(context.Background(), Env{Sys: s, Run: f}, cfg); err != nil || code != 0 {
		t.Fatalf("RunJob = %d, %v", code, err)
	}
	if _, ok := s.mountAt("/work"); ok {
		t.Error("selftest mounted a code disk")
	}
	if out, _ := s.mountAt("/out"); out.Source != "/dev/vdc" {
		t.Errorf("selftest output disk = %q, want /dev/vdc", out.Source)
	}
	job := f.Calls()[3]
	if job.Args[len(job.Args)-1] != "/usr/local/bin/cc-selftest" {
		t.Errorf("selftest ran %v", job.Args)
	}
	if got, _ := envValue(job.Env, "CC_PROBE_PORTS"); got != "8080,5432" {
		t.Errorf("CC_PROBE_PORTS = %q", got)
	}
	if _, ok := envValue(job.Env, "CC_MAX_BUDGET_USD"); ok {
		t.Error("selftest got a budget variable")
	}
}

func TestRunJobStopsWhenTheNetworkFails(t *testing.T) {
	s := newFakeSys()
	f := &system.FakeRunner{}
	f.On(ipPath, "link", "set", "lo", "up").Return(system.Result{})
	f.On(ipPath, "addr", "add").Return(system.Result{Code: 2, Stderr: []byte("RTNETLINK answers: File exists")})

	if _, err := RunJob(context.Background(), Env{Sys: s, Run: f}, reviewConfig()); err == nil {
		t.Fatal("RunJob carried on after the address failed")
	}
	if len(f.Calls()) != 2 {
		t.Errorf("ran %d commands after the failure", len(f.Calls()))
	}
}

func TestRunJobStopsWhenAMountFails(t *testing.T) {
	s := newFakeSys()
	s.mountErr["/work"] = syscall.EINVAL
	f := &system.FakeRunner{}
	if _, err := RunJob(context.Background(), Env{Sys: s, Run: f}, reviewConfig()); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("RunJob = %v, want the mount error", err)
	}
	if len(f.Calls()) != 0 {
		t.Error("RunJob ran commands after a mount failed")
	}
}

func TestRunJobRefusesBuildMode(t *testing.T) {
	if _, err := RunJob(context.Background(), Env{Sys: newFakeSys(), Run: &system.FakeRunner{}}, Config{Mode: ModeBuild, RunID: "b"}); err == nil {
		t.Error("RunJob accepted build mode")
	}
}
