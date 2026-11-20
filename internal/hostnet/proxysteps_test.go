package hostnet

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestProxyBinaryStep(t *testing.T) {
	r := newRig()
	r.write(t, "/home/u/bin/codecheck-sandbox", "binary v2", 0o755)
	r.write(t, "/usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox", "binary v1", 0o755)
	s := r.step(t, "host-proxy-binary")
	if check(t, s).OK {
		t.Fatal("an outdated copy passed")
	}
	r.run.On("install").Return(ok(0))
	mustApply(t, s)
	wantArgvs(t, r, "# install -D -m 0755 -o root -g root -T /home/u/bin/codecheck-sandbox /usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox")

	r = newRig()
	r.write(t, "/home/u/bin/codecheck-sandbox", "binary v2", 0o755)
	r.write(t, "/usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox", "binary v2", 0o755)
	if !check(t, r.step(t, "host-proxy-binary")).OK {
		t.Error("an identical copy failed")
	}
}

func TestProxyBinaryRecordsOnlyNewFolders(t *testing.T) {
	r := newRig()
	r.write(t, "/home/u/bin/codecheck-sandbox", "binary", 0o755)
	_ = r.fs.MkdirAll("/usr/local/lib", 0o755)
	r.run.On("install").Return(ok(0))
	mustApply(t, r.step(t, "host-proxy-binary"))
	var paths []string
	for _, it := range r.records {
		paths = append(paths, it.Path)
	}
	want := []string{"/usr/local/lib/codecheck-sandbox", "/usr/local/lib/codecheck-sandbox/bin", "/usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox"}
	if !slices.Equal(paths, want) {
		t.Errorf("records = %v, want %v", paths, want)
	}
}

func TestProxyCAStep(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-proxy-ca")
	if check(t, s).OK {
		t.Fatal("passed without a CA")
	}
	plan, err := s.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var certShown, keyMasked bool
	for _, f := range plan.Files {
		switch f.Path {
		case "/etc/codecheck-sandbox/ca/ca.crt":
			certShown = strings.Contains(string(f.Content), "BEGIN CERTIFICATE") && f.Mode == 0o644
		case "/etc/codecheck-sandbox/ca/ca.key":
			keyMasked = !strings.Contains(string(f.Content), "PRIVATE KEY") && f.Mode == 0o600
		}
	}
	if !certShown || !keyMasked {
		t.Errorf("plan files = %+v; want the certificate shown and the key masked", plan.Files)
	}
	r.run.On("install", "-d").Return(ok(0))
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	mustApply(t, s)
	calls := r.run.Calls()
	var keyBytes, certBytes string
	for i, c := range calls {
		if c.Name == "dd" && strings.Contains(c.Args[0], "ca.key") {
			keyBytes = string(c.Stdin)
			if calls[i-1].Args[1] != "0600" {
				t.Errorf("CA key written with mode %s, want 0600", calls[i-1].Args[1])
			}
		}
		if c.Name == "dd" && strings.Contains(c.Args[0], "ca.crt") {
			certBytes = string(c.Stdin)
		}
	}
	if !strings.Contains(keyBytes, "EC PRIVATE KEY") {
		t.Error("the real key never got written")
	}
	for _, f := range plan.Files {
		if strings.HasSuffix(f.Path, "ca.crt") && string(f.Content) != certBytes {
			t.Error("Apply wrote a different certificate from the one the plan showed")
		}
	}
}

func TestProxyCAStepChecksTheCertificate(t *testing.T) {
	r := newRig()
	r.write(t, "/etc/codecheck-sandbox/ca/ca.crt", "not a certificate", 0o644)
	r.write(t, "/etc/codecheck-sandbox/ca/ca.key", "", 0o600)
	if check(t, r.step(t, "host-proxy-ca")).OK {
		t.Error("a garbage certificate passed")
	}
}

func TestAPIKeyStep(t *testing.T) {
	r := newRig()
	r.apiKey = "sk-ant-api03-REALKEYREALKEY"
	s := r.step(t, "host-api-key")
	if check(t, s).OK {
		t.Fatal("passed without the key file")
	}
	plan, _ := s.Plan(context.Background())
	if len(plan.Files) != 1 || strings.Contains(string(plan.Files[0].Content), "REALKEY") || plan.Files[0].Mode != 0o600 {
		t.Errorf("plan = %+v; the key must stay hidden, mode 0600", plan.Files)
	}
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	mustApply(t, s)
	calls := r.run.Calls()
	if string(calls[1].Stdin) != "sk-ant-api03-REALKEYREALKEY\n" || calls[0].Args[1] != "0600" {
		t.Errorf("key write = %+v / %+v", calls[0], calls[1])
	}

	r = newRig()
	r.write(t, "/etc/codecheck-sandbox/api-key", "x", 0o600)
	if !check(t, r.step(t, "host-api-key")).OK {
		t.Error("an existing key file failed")
	}
}

func TestAPIKeyStepRefusesBadKeys(t *testing.T) {
	for _, key := range []string{"", "sk-ant-sandbox-placeholder", "not-a-key", "sk-ant-api03-a\nb"} {
		r := newRig()
		r.apiKey = key
		if err := r.step(t, "host-api-key").Apply(context.Background()); err == nil {
			t.Errorf("key %q accepted", key)
		}
		if len(r.run.Calls()) != 0 {
			t.Errorf("key %q: wrote something", key)
		}
	}
}

func TestProxyServiceStep(t *testing.T) {
	r := newRig()
	s := r.step(t, "host-proxy-service")
	if check(t, s).OK {
		t.Fatal("passed without the unit")
	}
	r.run.On("install").Return(ok(0))
	r.run.On("dd").Return(ok(0))
	r.run.On("systemctl").Return(ok(0))
	r.run.On("systemctl").Return(ok(0))
	r.run.On("systemctl").Return(ok(0))
	mustApply(t, s)
	got := r.argvs()
	if !slices.Equal(got[2:], []string{"# systemctl daemon-reload", "# systemctl enable codecheck-proxy.service", "# systemctl restart codecheck-proxy.service"}) {
		t.Errorf("commands = %v", got)
	}

	r = newRig()
	r.write(t, "/etc/systemd/system/codecheck-proxy.service", rendered(t, "codecheck-proxy.service"), 0o644)
	r.run.On("systemctl", "is-enabled").Return(ok(0))
	r.run.On("systemctl", "is-active").Return(ok(0))
	if !check(t, r.step(t, "host-proxy-service")).OK {
		t.Error("an enabled, active, current unit failed")
	}
}
