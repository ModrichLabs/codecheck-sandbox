package hostnet

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/proxy"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

func TestProxyServiceRestartsAfterANewBinary(t *testing.T) {
	r := newRig()
	r.write(t, "/home/u/bin/codecheck-sandbox", "binary v2", 0o755)
	r.write(t, "/etc/systemd/system/codecheck-proxy.service", rendered(t, "codecheck-proxy.service"), 0o644)
	steps, _ := Steps(r.deps())
	var bin, svc = steps[7], steps[10]
	if bin.ID() != "host-proxy-binary" || svc.ID() != "host-proxy-service" {
		t.Fatalf("unexpected order: %s, %s", bin.ID(), svc.ID())
	}
	r.run.On("install").Return(ok(0))
	if err := bin.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The unit file matches and the service runs, but it runs the old binary.
	st, err := svc.Check(context.Background())
	if err != nil || st.OK {
		t.Errorf("proxy service check = %+v, %v; want a restart after a new binary", st, err)
	}
}

func TestAPIKeyStepRewritesASuppliedKey(t *testing.T) {
	r := newRig()
	r.apiKey = "sk-ant-api03-NEWKEY"
	r.write(t, "/etc/codecheck-sandbox/api-key", "old", 0o600)
	if check(t, r.step(t, "host-api-key")).OK {
		t.Error("a new key from the wizard left the old one in place")
	}
	r = newRig() // no key supplied, as under doctor
	r.write(t, "/etc/codecheck-sandbox/api-key", "old", 0o600)
	d := r.deps()
	d.APIKey = nil
	steps, _ := Steps(d)
	for _, s := range steps {
		if s.ID() == "host-api-key" {
			if st, _ := s.Check(context.Background()); !st.OK {
				t.Errorf("with no new key, a stored key failed: %s", st.Detail)
			}
		}
	}
}

func TestProxyCAStepChecksTheKeyMatches(t *testing.T) {
	certPEM, keyPEM, _ := proxy.NewCA(time.Now())
	_, otherKey, _ := proxy.NewCA(time.Now())

	r := newRig()
	r.write(t, "/etc/codecheck-sandbox/ca/ca.crt", string(certPEM), 0o644)
	r.write(t, "/etc/codecheck-sandbox/ca/ca.key", "", 0o600)
	r.run.On("cat").Return(system.Result{Stdout: keyPEM})
	if st := check(t, r.step(t, "host-proxy-ca")); !st.OK {
		t.Errorf("a matching pair failed: %s", st.Detail)
	}
	if c := r.run.Calls()[0]; !c.Root || !slices.Equal(c.Args, []string{"--", "/etc/codecheck-sandbox/ca/ca.key"}) {
		t.Errorf("key read = %+v, want cat as root", c)
	}

	r = newRig()
	r.write(t, "/etc/codecheck-sandbox/ca/ca.crt", string(certPEM), 0o644)
	r.write(t, "/etc/codecheck-sandbox/ca/ca.key", "", 0o600)
	r.run.On("cat").Return(system.Result{Stdout: otherKey})
	if check(t, r.step(t, "host-proxy-ca")).OK {
		t.Error("a key that doesn't match the certificate passed")
	}
}

func TestProxyStepsRelabelUnderSELinux(t *testing.T) {
	r := newRig()
	r.write(t, "/sys/fs/selinux/enforce", "1", 0o644)
	r.write(t, "/home/u/bin/codecheck-sandbox", "binary", 0o755)
	r.run.On("install").Return(ok(0))
	r.run.On("restorecon").Return(ok(0))
	mustApply(t, r.step(t, "host-proxy-binary"))
	got := r.argvs()
	if len(got) != 2 || !strings.HasPrefix(got[1], "# restorecon -F -- /usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox") {
		t.Errorf("commands = %v; want the binary relabelled before any service runs it", got)
	}
}
