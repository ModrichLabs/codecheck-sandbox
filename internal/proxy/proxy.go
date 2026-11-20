package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Placeholder stands in for the API key inside the guest; the proxy swaps it
// for the real key and refuses anything else.
const Placeholder = "sk-ant-sandbox-placeholder"

// Default caps. Requests and bytes count per tunnel, and keep-alive lets a
// whole review share one tunnel, so these stay generous. They bound what one
// tunnel carries, not a run: a client can open another tunnel, up to
// DefaultMaxTunnels at once. --max-budget-usd bounds a run's spend.
const (
	DefaultMaxRequests = 5000
	DefaultMaxBytes    = 2 << 30
	DefaultMaxTunnels  = 16
)

// allowed lists the paths Claude Code needs (spike S4), with their methods.
var allowed = map[string]string{
	"/v1/messages":                   http.MethodPost,
	"/api/claude_code/settings":      http.MethodGet,
	"/api/claude_code/policy_limits": http.MethodGet,
}

// Config sets up a Proxy.
type Config struct {
	// Client names the one address allowed to connect: the guest.
	Client netip.Addr
	// RealKey replaces the placeholder on the way out. It never reaches a log.
	RealKey string
	Leaves  *LeafCache
	Deny    *DenyLog
	Log     *slog.Logger // nil discards
	// Upstream carries requests to the real API; nil means a default
	// transport with system roots.
	Upstream    http.RoundTripper
	MaxRequests int64 // per tunnel; zero means DefaultMaxRequests
	MaxBytes    int64 // per tunnel, both directions; zero means DefaultMaxBytes
	MaxTunnels  int64 // open at once; zero means DefaultMaxTunnels
}

// Proxy implements the egress proxy as an http.Handler.
type Proxy struct {
	cfg     Config
	rp      *httputil.ReverseProxy
	tunnels atomic.Int64
}

// New checks cfg and builds a Proxy.
func New(cfg Config) (*Proxy, error) {
	switch {
	case !cfg.Client.IsValid():
		return nil, errors.New("proxy: no client address")
	case cfg.RealKey == "" || cfg.RealKey == Placeholder:
		return nil, errors.New("proxy: no real API key")
	case cfg.Leaves == nil:
		return nil, errors.New("proxy: no CA leaves")
	case cfg.Deny == nil:
		return nil, errors.New("proxy: no denial log")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Upstream == nil {
		cfg.Upstream = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 15 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	if cfg.MaxRequests <= 0 {
		cfg.MaxRequests = DefaultMaxRequests
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.MaxTunnels <= 0 {
		cfg.MaxTunnels = DefaultMaxTunnels
	}
	p := &Proxy{cfg: cfg}
	target := &url.URL{Scheme: "https", Host: APIHost}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = APIHost
			pr.Out.Header.Set("X-Api-Key", cfg.RealKey)
		},
		Transport:     cfg.Upstream,
		FlushInterval: -1, // stream server-sent events as they come
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A request the guest itself abandoned refuses nothing.
			if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			p.deny(Denial{Reason: FailedUpstream, Client: r.RemoteAddr, Method: r.Method, Path: r.URL.Path})
			p.cfg.Log.Warn("proxy: upstream failed", "path", r.URL.Path, "err", p.scrub(err.Error()))
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return p, nil
}

func (p *Proxy) deny(d Denial) { p.cfg.Deny.Deny(d) }

// scrub keeps the real key out of any text bound for a log.
func (p *Proxy) scrub(s string) string { return strings.ReplaceAll(s, p.cfg.RealKey, "[redacted]") }

// ServeHTTP admits only CONNECT to the API host from the client address,
// then serves the tunnel's requests itself.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	refuse := func(reason Reason, code int) {
		p.deny(Denial{Reason: reason, Client: r.RemoteAddr, Method: r.Method, Host: r.Host, Path: r.URL.Path})
		http.Error(w, http.StatusText(code), code)
	}
	src, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || src.Addr().Unmap() != p.cfg.Client {
		refuse(DeniedSource, http.StatusForbidden)
		return
	}
	if r.Method != http.MethodConnect {
		refuse(DeniedMethod, http.StatusForbidden)
		return
	}
	if !strings.EqualFold(r.Host, APIHost+":443") {
		refuse(DeniedHost, http.StatusForbidden)
		return
	}
	if p.tunnels.Add(1) > p.cfg.MaxTunnels {
		p.tunnels.Add(-1)
		refuse(CapTunnels, http.StatusServiceUnavailable)
		return
	}
	defer p.tunnels.Add(-1)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunnel unavailable", http.StatusInternalServerError)
		return
	}
	raw, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	var conn net.Conn = raw
	if brw.Reader.Buffered() > 0 { // the client sent its TLS hello early
		conn = &bufConn{Conn: raw, r: brw.Reader}
	}
	if _, err := raw.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		raw.Close()
		return
	}
	p.tunnel(conn, r.RemoteAddr)
}

// tunnel terminates TLS on conn and serves its requests until it closes.
func (p *Proxy) tunnel(conn net.Conn, client string) {
	counted := &countingConn{Conn: conn, max: p.cfg.MaxBytes, over: func() {
		p.deny(Denial{Reason: CapBytes, Client: client})
	}}
	tc := tls.Server(counted, &tls.Config{
		GetCertificate: p.cfg.Leaves.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"http/1.1"},
	})
	_ = tc.SetDeadline(time.Now().Add(15 * time.Second))
	if err := tc.Handshake(); err != nil {
		p.deny(Denial{Reason: FailedTLS, Client: client})
		tc.Close()
		return
	}
	_ = tc.SetDeadline(time.Time{})

	var requests atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refuse := func(reason Reason, code int) {
			p.deny(Denial{Reason: reason, Client: client, Method: r.Method, Path: r.URL.Path})
			http.Error(w, http.StatusText(code), code)
		}
		if requests.Add(1) > p.cfg.MaxRequests {
			w.Header().Set("Connection", "close")
			refuse(CapRequests, http.StatusTooManyRequests)
			return
		}
		if allowed[r.URL.Path] != r.Method {
			refuse(DeniedPath, http.StatusForbidden)
			return
		}
		keys := r.Header.Values("X-Api-Key")
		if len(keys) != 1 || keys[0] != Placeholder || len(r.Header.Values("Authorization")) != 0 {
			refuse(DeniedKey, http.StatusUnauthorized)
			return
		}
		p.cfg.Log.Debug("proxy: forwarding", "method", r.Method, "path", r.URL.Path)
		p.rp.ServeHTTP(w, r)
	})
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       5 * time.Minute,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	_ = srv.Serve(newOneConnListener(tc))
}

// countingConn closes the tunnel once it has carried max bytes either way.
type countingConn struct {
	net.Conn
	max  int64
	n    atomic.Int64
	once sync.Once
	over func()
}

func (c *countingConn) add(n int) error {
	if c.n.Add(int64(n)) > c.max {
		c.once.Do(func() {
			c.over()
			c.Conn.Close()
		})
		return fmt.Errorf("tunnel passed its %d-byte cap", c.max)
	}
	return nil
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if capErr := c.add(n); capErr != nil {
		return n, capErr
	}
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	if err := c.add(len(b)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

// bufConn replays bytes the hijack buffered before reading the connection.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// oneConnListener hands http.Server a single connection, then blocks until
// that connection closes.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func newOneConnListener(c net.Conn) *oneConnListener {
	return &oneConnListener{conn: c, done: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = &closeConn{Conn: l.conn, done: l.done} })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

type closeConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *closeConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
