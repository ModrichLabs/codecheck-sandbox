package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ModrichLabs/codecheck-sandbox/internal/proxy"
)

// proxyFlags hold the hidden proxy command's flags.
type proxyFlags struct {
	listen, client, caCert, denyLog string
}

// proxySettings holds everything the proxy needs, loaded and checked.
type proxySettings struct {
	listen string
	cfg    proxy.Config
	deny   string
}

func newProxyCommand() *cobra.Command {
	f := proxyFlags{}
	c := &cobra.Command{
		Use:   "proxy",
		Short: "Run the egress proxy (started by systemd)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := loadProxySettings(f, os.Getenv("CREDENTIALS_DIRECTORY"))
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			return serveProxy(ctx, s)
		},
	}
	fl := c.Flags()
	fl.StringVar(&f.listen, "listen", "172.16.0.1:3180", "address and port to listen on (the tap's host address)")
	fl.StringVar(&f.client, "client", "172.16.0.2", "the guest address, the only client allowed")
	fl.StringVar(&f.caCert, "ca-cert", "/etc/codecheck-sandbox/ca/ca.crt", "the sandbox CA certificate")
	fl.StringVar(&f.denyLog, "deny-log", "/var/log/codecheck-sandbox/proxy.jsonl", "where refusals get logged")
	return c
}

// loadProxySettings checks the flags and reads the two credentials systemd
// passes through credsDir (LoadCredential=): the API key and the CA key.
// Errors never repeat a credential's content.
func loadProxySettings(f proxyFlags, credsDir string) (proxySettings, error) {
	listen, err := netip.ParseAddrPort(f.listen)
	if err != nil || !listen.Addr().Is4() || listen.Addr().IsUnspecified() {
		return proxySettings{}, fmt.Errorf("--listen %q: want one IPv4 address and a port, never a wildcard", f.listen)
	}
	client, err := netip.ParseAddr(f.client)
	if err != nil || !client.Is4() || client.IsUnspecified() {
		return proxySettings{}, fmt.Errorf("--client %q: want the guest's IPv4 address", f.client)
	}
	if credsDir == "" {
		return proxySettings{}, errors.New("no CREDENTIALS_DIRECTORY: the proxy runs under systemd with LoadCredential=")
	}
	key, err := readCredential(credsDir, "api-key")
	if err != nil {
		return proxySettings{}, err
	}
	if key == proxy.Placeholder {
		return proxySettings{}, errors.New("credential api-key holds the guest placeholder, not a real key")
	}
	caKey, err := readCredential(credsDir, "ca-key")
	if err != nil {
		return proxySettings{}, err
	}
	caCert, err := os.ReadFile(f.caCert)
	if err != nil {
		return proxySettings{}, fmt.Errorf("reading the CA certificate: %w", err)
	}
	ca, err := proxy.LoadCA(caCert, []byte(caKey+"\n"))
	if err != nil {
		return proxySettings{}, err
	}
	return proxySettings{
		listen: listen.String(),
		deny:   f.denyLog,
		cfg:    proxy.Config{Client: client, RealKey: key, Leaves: proxy.NewLeafCache(ca, nil)},
	}, nil
}

func readCredential(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("credential %s: %w", name, errors.Unwrap(err))
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("credential %s: empty", name)
	}
	return v, nil
}

// serveProxy listens and serves until ctx ends.
func serveProxy(ctx context.Context, s proxySettings) error {
	deny, err := proxy.OpenDenyLog(s.deny, proxy.DefaultDenyLogSize)
	if err != nil {
		return err
	}
	defer deny.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	s.cfg.Deny, s.cfg.Log = deny, log
	p, err := proxy.New(s.cfg)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", s.listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Info("proxy: listening", "addr", s.listen, "client", s.cfg.Client.String())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
