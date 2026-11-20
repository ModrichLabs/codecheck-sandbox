package proxy

import (
	"bytes"
	"context"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTunnelLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxTunnels = 1 })
	first := h.client()
	if resp, err := h.call(t, first, "POST", "/v1/messages", Placeholder, nil); err != nil || resp.StatusCode != 200 {
		t.Fatalf("first tunnel: %v %v", resp, err)
	}
	// first's tunnel stays open (keep-alive); a second client needs another.
	if _, err := h.call(t, h.client(), "POST", "/v1/messages", Placeholder, nil); err == nil {
		t.Fatal("a second tunnel opened past the limit of one")
	}
	if !strings.Contains(h.denials.String(), `"reason":"cap-tunnels"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
	first.CloseIdleConnections()
}

func TestCancelledRequestsLogNothing(t *testing.T) {
	certPEM, keyPEM, _ := NewCA(time.Now())
	ca, _ := LoadCA(certPEM, keyPEM)
	var denials bytes.Buffer
	p, err := New(Config{Client: netip.MustParseAddr("127.0.0.1"), RealKey: realKey, Leaves: NewLeafCache(ca, nil), Deny: NewDenyLog(&denials)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	p.rp.ErrorHandler(httptest.NewRecorder(), r, context.Canceled)
	if denials.Len() != 0 {
		t.Errorf("a request the guest cancelled reached the denial log: %s", denials.String())
	}
}

func TestDenyLogStaysRootOnly(t *testing.T) {
	path := t.TempDir() + "/proxy.jsonl"
	l, err := OpenDenyLog(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	l.Deny(Denial{Reason: DeniedHost})
	_ = l.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v; want 0600", info.Mode().Perm(), err)
	}
}
