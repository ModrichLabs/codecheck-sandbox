package system

import (
	"context"
	"strings"
	"testing"
	"time"
)

func runOK(t *testing.T, c Cmd) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := ExecRunner{}.Run(ctx, c)
	if err != nil {
		t.Fatalf("Run(%v): unexpected error: %v", c, err)
	}
	return res
}

func TestExecRunnerPassesArgumentsVerbatim(t *testing.T) {
	// A shell would expand the glob and the variable; ExecRunner must not.
	res := runOK(t, Cmd{Name: "printf", Args: []string{"%s|", "a b", "*", "$HOME", ""}})
	if got, want := string(res.Stdout), "a b|*|$HOME||"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestExecRunnerForcesCLocale(t *testing.T) {
	res := runOK(t, Cmd{Name: "env", Env: []string{"LC_ALL=en_AU.UTF-8", "CC_TEST=yes"}})
	lines := strings.Split(string(res.Stdout), "\n")
	var lcAll []string
	var sawExtra bool
	for _, l := range lines {
		if strings.HasPrefix(l, "LC_ALL=") {
			lcAll = append(lcAll, l)
		}
		if l == "CC_TEST=yes" {
			sawExtra = true
		}
	}
	if len(lcAll) != 1 || lcAll[0] != "LC_ALL=C" {
		t.Errorf("LC_ALL entries = %v, want exactly [LC_ALL=C]", lcAll)
	}
	if !sawExtra {
		t.Errorf("env output lacks CC_TEST=yes from Cmd.Env")
	}
}

func TestExecRunnerReportsExitCode(t *testing.T) {
	res := runOK(t, Cmd{Name: "sh", Args: []string{"-c", "echo oops >&2; exit 7"}})
	if res.Code != 7 {
		t.Errorf("Code = %d, want 7", res.Code)
	}
	if got := string(res.Stderr); got != "oops\n" {
		t.Errorf("stderr = %q, want %q", got, "oops\n")
	}
}

func TestExecRunnerZeroExitCode(t *testing.T) {
	if res := runOK(t, Cmd{Name: "true"}); res.Code != 0 {
		t.Errorf("Code = %d, want 0", res.Code)
	}
}

func TestExecRunnerEmptyStdinReadsDevNull(t *testing.T) {
	res := runOK(t, Cmd{Name: "readlink", Args: []string{"/proc/self/fd/0"}})
	if got := strings.TrimSpace(string(res.Stdout)); got != "/dev/null" {
		t.Errorf("stdin points at %q, want /dev/null", got)
	}
}

func TestExecRunnerFeedsStdin(t *testing.T) {
	res := runOK(t, Cmd{Name: "cat", Stdin: []byte("secret bytes")})
	if got := string(res.Stdout); got != "secret bytes" {
		t.Errorf("stdout = %q, want %q", got, "secret bytes")
	}
}

func TestExecRunnerMissingCommandErrors(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), Cmd{Name: "codecheck-sandbox-no-such-tool"})
	if err == nil {
		t.Fatal("expected an error for a missing command")
	}
}

func TestExecRunnerRefusesRoot(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), Cmd{Name: "true", Root: true})
	if err == nil {
		t.Fatal("expected ExecRunner to refuse a Root command")
	}
}

func TestExecRunnerHonoursContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecRunner{}.Run(ctx, Cmd{Name: "sleep", Args: []string{"5"}})
	if err == nil {
		t.Fatal("expected an error once the context ends")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("Run took %v after the context ended", time.Since(start))
	}
}
