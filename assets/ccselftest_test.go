package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sourceSelftest runs snippet in bash after sourcing cc-selftest, which
// defines its helpers without running any probe.
func sourceSelftest(t *testing.T, snippet string, env ...string) (string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cc-selftest")
	if err := os.WriteFile(path, []byte(readGuest(t, "cc-selftest")), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `source "$1"; `+snippet, "bash", path)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("bash: %v", err)
	}
	return string(out), code
}

func TestSelftestSourcingRunsNoProbe(t *testing.T) {
	out, code := sourceSelftest(t, "true")
	if out != "" || code != 0 {
		t.Errorf("sourcing printed %q and exited %d; want silence", out, code)
	}
}

func TestSelftestVerdicts(t *testing.T) {
	out, code := sourceSelftest(t, `
blocked held-a "a blocked path" false
blocked broke-b "a path that opened" true
allowed works-c "a path that must work" true
allowed broke-d "a path that must work but failed" false
summary`)
	for _, want := range []string{
		"PASS held-a a blocked path",
		"FAIL broke-b a path that opened",
		"PASS works-c a path that must work",
		"FAIL broke-d a path that must work but failed",
		"SUMMARY 2 passed, 2 failed",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code != 1 {
		t.Errorf("summary exited %d after failures, want 1", code)
	}
}

func TestSelftestAllPassExitsZero(t *testing.T) {
	out, code := sourceSelftest(t, `blocked a "x" false; summary`)
	if code != 0 || !strings.Contains(out, "SUMMARY 1 passed, 0 failed") {
		t.Errorf("exit %d, output %q", code, out)
	}
}

// A probe that checks for silence must not pass because its command broke,
// such as ip naming a link that doesn't exist.
func TestSelftestNoOutputNeedsTheCommandToWork(t *testing.T) {
	out, _ := sourceSelftest(t, `
no_output true && echo quiet-ok
no_output false || echo broken-fails
no_output echo noise || echo noisy-fails
no_output sh -c 'echo gone >&2; exit 1' || echo stderr-fails`)
	for _, want := range []string{"quiet-ok", "broken-fails", "noisy-fails", "stderr-fails"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("output lacks %s:\n%s", want, out)
		}
	}
	s := readGuest(t, "cc-selftest")
	if strings.Contains(s, "has_output") {
		t.Error("cc-selftest still uses has_output, which passes when ip fails")
	}
}

func TestSelftestPortList(t *testing.T) {
	out, _ := sourceSelftest(t, `host_ports`, "CC_PROBE_PORTS=8080,5432,22")
	ports := strings.Fields(out)
	for _, want := range []string{"22", "80", "443", "2375", "8080", "5432"} {
		if !contains(ports, want) {
			t.Errorf("host_ports lacks %s: %v", want, ports)
		}
	}
	if contains(ports, "3180") {
		t.Error("host_ports includes the proxy port, which must answer")
	}
	if strings.Count(" "+out+" ", " 22 ") != 1 {
		t.Errorf("host_ports repeats a port: %q", out)
	}
	// Anything but digits never reaches /dev/tcp.
	if out, code := sourceSelftest(t, `host_ports`, "CC_PROBE_PORTS=80,1;reboot"); code == 0 || strings.Contains(out, "reboot") {
		t.Errorf("host_ports accepted junk: %q (exit %d)", out, code)
	}
}

func TestSelftestHasEveryProbe(t *testing.T) {
	s := readGuest(t, "cc-selftest")
	for _, id := range []string{
		// The eight original probes.
		"no-direct-internet", "no-host-ssh", "no-dns", "proxy-blocks-github",
		"work-read-only", "root-read-only", "config-hidden", "proxy-allows-anthropic",
		// The extensions.
		"host-port-", "docker-bridge-", "no-ipv6", "no-default-route",
		"no-spoofed-source", "no-sudo",
	} {
		if !strings.Contains(s, id) {
			t.Errorf("cc-selftest lacks the %s probe", id)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
