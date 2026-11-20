package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const realKey = "sk-ant-api03-REALKEYREALKEYREALKEY"

// upstream stands in for api.anthropic.com: a TLS server whose certificate
// names the API host, signed by its own CA.
type upstream struct {
	srv   *httptest.Server
	roots *x509.CertPool
	mu    sync.Mutex
	seen  []*http.Request
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	u := &upstream{}
	certPEM, keyPEM, _ := NewCA(time.Now())
	ca, _ := LoadCA(certPEM, keyPEM)
	leaf, _ := ca.Leaf(time.Now())
	u.roots = x509.NewCertPool()
	u.roots.AddCert(ca.Cert)
	u.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, r.Clone(context.Background()))
		u.mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message"}`))
	}))
	u.srv.TLS = &tls.Config{Certificates: []tls.Certificate{*leaf}}
	u.srv.StartTLS()
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) requests() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.seen...)
}

// transport dials the fake upstream whatever address the proxy asks for.
func (u *upstream) transport() http.RoundTripper {
	addr := u.srv.Listener.Addr().String()
	return &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: u.roots},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
}

type harness struct {
	up      *upstream
	proxy   *httptest.Server
	sandbox *x509.CertPool
	logs    lockedBuffer
	denials lockedBuffer
}

// lockedBuffer lets the test read a log while the proxy's goroutines write it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	h := &harness{up: newUpstream(t)}
	certPEM, keyPEM, _ := NewCA(time.Now())
	ca, _ := LoadCA(certPEM, keyPEM)
	h.sandbox = x509.NewCertPool()
	h.sandbox.AddCert(ca.Cert)
	cfg := Config{
		Client:      netip.MustParseAddr("127.0.0.1"),
		RealKey:     realKey,
		Leaves:      NewLeafCache(ca, nil),
		Deny:        NewDenyLog(&h.denials),
		Log:         slog.New(slog.NewJSONHandler(&h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Upstream:    h.up.transport(),
		MaxRequests: 100,
		MaxBytes:    1 << 20,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.proxy = httptest.NewServer(p)
	t.Cleanup(h.proxy.Close)
	return h
}

// client behaves like Claude Code in the guest: HTTPS_PROXY plus the
// sandbox CA in its trust store.
func (h *harness) client() *http.Client {
	pu, _ := url.Parse(h.proxy.URL)
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(pu),
		TLSClientConfig: &tls.Config{RootCAs: h.sandbox},
	}, Timeout: 10 * time.Second}
}

func (h *harness) call(t *testing.T, c *http.Client, method, path, key string, extra http.Header) (*http.Response, error) {
	t.Helper()
	req, _ := http.NewRequest(method, "https://api.anthropic.com"+path, strings.NewReader(`{"model":"x"}`))
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	for k, v := range extra {
		req.Header[k] = v
	}
	resp, err := c.Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	return resp, err
}

func (h *harness) noKeyLeaked(t *testing.T) {
	t.Helper()
	for name, b := range map[string]string{"log": h.logs.String(), "denial log": h.denials.String()} {
		if strings.Contains(b, realKey) || strings.Contains(b, "REALKEY") {
			t.Errorf("the real key reached the %s:\n%s", name, b)
		}
	}
}

func TestAllowedHostSwapsThePlaceholder(t *testing.T) {
	h := newHarness(t, nil)
	c := h.client()
	for _, call := range []struct{ method, path string }{
		{"POST", "/v1/messages?beta=true"},
		{"GET", "/api/claude_code/settings"},
		{"GET", "/api/claude_code/policy_limits"},
	} {
		resp, err := h.call(t, c, call.method, call.path, Placeholder, nil)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s %s: %v %v", call.method, call.path, resp, err)
		}
	}
	reqs := h.up.requests()
	if len(reqs) != 3 {
		t.Fatalf("upstream saw %d requests, want 3", len(reqs))
	}
	for _, r := range reqs {
		if r.Header.Get("x-api-key") != realKey {
			t.Errorf("%s reached upstream with x-api-key %q, want the real key", r.URL.Path, r.Header.Get("x-api-key"))
		}
		if r.Host != APIHost {
			t.Errorf("upstream Host = %q", r.Host)
		}
		for k := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-forwarded") || strings.EqualFold(k, "forwarded") {
				t.Errorf("upstream got %s, which would reveal the guest", k)
			}
		}
	}
	h.noKeyLeaked(t)
}

func TestDeniedHost(t *testing.T) {
	h := newHarness(t, nil)
	req, _ := http.NewRequest("GET", "https://example.com/", nil)
	if _, err := h.client().Do(req); err == nil {
		t.Fatal("a CONNECT to example.com got through")
	}
	if !strings.Contains(h.denials.String(), `"reason":"denied-host"`) || !strings.Contains(h.denials.String(), "example.com:443") {
		t.Errorf("denial log = %s", h.denials.String())
	}
	if len(h.up.requests()) != 0 {
		t.Error("upstream saw a request")
	}
}

func TestDeniedSource(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Client = netip.MustParseAddr("172.16.0.2") })
	if _, err := h.call(t, h.client(), "POST", "/v1/messages", Placeholder, nil); err == nil {
		t.Fatal("a client from the wrong address got through")
	}
	if !strings.Contains(h.denials.String(), `"reason":"denied-source"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
}

func TestPlainRequestsRefused(t *testing.T) {
	h := newHarness(t, nil)
	pu, _ := url.Parse(h.proxy.URL)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := c.Get("http://api.anthropic.com/v1/messages")
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain GET: %v %v, want 403", resp, err)
	}
	if !strings.Contains(h.denials.String(), `"reason":"denied-method"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
}

func TestWrongKeyRejected(t *testing.T) {
	h := newHarness(t, nil)
	c := h.client()
	cases := map[string]struct {
		key   string
		extra http.Header
	}{
		"an attacker's own key":   {"sk-ant-api03-ATTACKER", nil},
		"no key":                  {"", nil},
		"an Authorization header": {Placeholder, http.Header{"Authorization": {"Bearer sk-ant-api03-ATTACKER"}}},
		"two x-api-key headers":   {Placeholder, http.Header{"X-Api-Key": {Placeholder, "sk-ant-api03-ATTACKER"}}},
	}
	for name, tc := range cases {
		resp, err := h.call(t, c, "POST", "/v1/messages", tc.key, tc.extra)
		if err != nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %v %v, want 401", name, resp, err)
		}
	}
	if n := len(h.up.requests()); n != 0 {
		t.Errorf("upstream saw %d requests with a wrong key", n)
	}
	if !strings.Contains(h.denials.String(), `"reason":"denied-key"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
	if strings.Contains(h.denials.String()+h.logs.String(), "ATTACKER") {
		t.Error("a refused key reached a log")
	}
}

func TestOtherPathsRefused(t *testing.T) {
	h := newHarness(t, nil)
	c := h.client()
	for _, call := range []struct{ method, path string }{
		{"GET", "/v1/files"},
		{"POST", "/v1/messages/batches"},
		{"GET", "/v1/messages"},
		{"POST", "/api/claude_code/settings"},
	} {
		resp, err := h.call(t, c, call.method, call.path, Placeholder, nil)
		if err != nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s: %v %v, want 403", call.method, call.path, resp, err)
		}
	}
	if n := len(h.up.requests()); n != 0 {
		t.Errorf("upstream saw %d requests on refused paths", n)
	}
	if !strings.Contains(h.denials.String(), `"path":"/v1/files"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
}

func TestRequestCap(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRequests = 2 })
	c := h.client()
	for i := range 2 {
		if resp, err := h.call(t, c, "POST", "/v1/messages", Placeholder, nil); err != nil || resp.StatusCode != 200 {
			t.Fatalf("request %d: %v %v", i, resp, err)
		}
	}
	resp, err := h.call(t, c, "POST", "/v1/messages", Placeholder, nil)
	if err == nil && resp.StatusCode == 200 {
		t.Fatal("a third request on one tunnel got through")
	}
	if len(h.up.requests()) != 2 {
		t.Errorf("upstream saw %d requests, want 2", len(h.up.requests()))
	}
	if !strings.Contains(h.denials.String(), `"reason":"cap-requests"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
}

func TestByteCap(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxBytes = 4096 })
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", strings.NewReader(strings.Repeat("x", 64<<10)))
	req.Header.Set("x-api-key", Placeholder)
	resp, err := h.client().Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("a 64 KiB request passed a 4 KiB cap")
		}
	}
	if !strings.Contains(h.denials.String(), `"reason":"cap-bytes"`) {
		t.Errorf("denial log = %s", h.denials.String())
	}
}

func TestUpstreamFailureKeepsTheKeyOut(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Upstream = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: io.ErrUnexpectedEOF}
		}}
	})
	resp, err := h.call(t, h.client(), "POST", "/v1/messages", Placeholder, nil)
	if err != nil || resp.StatusCode != http.StatusBadGateway {
		t.Errorf("unreachable upstream: %v %v, want 502", resp, err)
	}
	h.noKeyLeaked(t)
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	certPEM, keyPEM, _ := NewCA(time.Now())
	ca, _ := LoadCA(certPEM, keyPEM)
	good := Config{Client: netip.MustParseAddr("172.16.0.2"), RealKey: realKey, Leaves: NewLeafCache(ca, nil), Deny: NewDenyLog(io.Discard)}
	bad := []func(*Config){
		func(c *Config) { c.RealKey = "" },
		func(c *Config) { c.RealKey = Placeholder },
		func(c *Config) { c.Client = netip.Addr{} },
		func(c *Config) { c.Leaves = nil },
		func(c *Config) { c.Deny = nil },
	}
	if _, err := New(good); err != nil {
		t.Fatalf("New(good): %v", err)
	}
	for i, mutate := range bad {
		c := good
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Errorf("case %d: New accepted %+v", i, c)
		}
	}
}
