package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/proxy"
)

// creds writes a credentials folder and a CA certificate, as systemd and
// setup would, and returns the folder and the certificate's path.
func creds(t *testing.T, apiKey string, withCAKey bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	certPEM, keyPEM, err := proxy.NewCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "-" {
		_ = os.WriteFile(filepath.Join(dir, "api-key"), []byte(apiKey), 0o600)
	}
	if withCAKey {
		_ = os.WriteFile(filepath.Join(dir, "ca-key"), keyPEM, 0o600)
	}
	certPath := filepath.Join(t.TempDir(), "ca.crt")
	_ = os.WriteFile(certPath, certPEM, 0o644)
	return dir, certPath
}

func validFlags(cert string) proxyFlags {
	return proxyFlags{listen: "172.16.0.1:3180", client: "172.16.0.2", caCert: cert, denyLog: filepath.Join(os.TempDir(), "unused.jsonl")}
}

func TestLoadProxySettings(t *testing.T) {
	dir, cert := creds(t, "sk-ant-api03-REAL\n", true)
	s, err := loadProxySettings(validFlags(cert), dir)
	if err != nil {
		t.Fatalf("loadProxySettings: %v", err)
	}
	if s.cfg.RealKey != "sk-ant-api03-REAL" {
		t.Errorf("key = %q; want it trimmed of the trailing newline", s.cfg.RealKey)
	}
	if s.cfg.Client.String() != "172.16.0.2" || s.listen != "172.16.0.1:3180" {
		t.Errorf("client %s, listen %s", s.cfg.Client, s.listen)
	}
	if s.cfg.Leaves == nil {
		t.Error("no CA leaves")
	}
}

func TestProxyFlagValidation(t *testing.T) {
	dir, cert := creds(t, "sk-ant-api03-REAL", true)
	bad := map[string]func(*proxyFlags){
		"listen on every address": func(f *proxyFlags) { f.listen = "0.0.0.0:3180" },
		"listen without a port":   func(f *proxyFlags) { f.listen = "172.16.0.1" },
		"listen on a name":        func(f *proxyFlags) { f.listen = "localhost:3180" },
		"listen on IPv6 any":      func(f *proxyFlags) { f.listen = "[::]:3180" },
		"client by name":          func(f *proxyFlags) { f.client = "guest" },
		"client unspecified":      func(f *proxyFlags) { f.client = "0.0.0.0" },
		"no CA certificate":       func(f *proxyFlags) { f.caCert = filepath.Join(t.TempDir(), "missing.crt") },
	}
	for name, mutate := range bad {
		f := validFlags(cert)
		mutate(&f)
		if _, err := loadProxySettings(f, dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestProxyCredentials(t *testing.T) {
	cases := map[string]struct {
		dir     func(t *testing.T) (string, string)
		wantErr string
	}{
		"no credentials folder": {func(t *testing.T) (string, string) { _, c := creds(t, "x", true); return "", c }, "CREDENTIALS_DIRECTORY"},
		"no api-key":            {func(t *testing.T) (string, string) { return creds(t, "-", true) }, "api-key"},
		"an empty api-key":      {func(t *testing.T) (string, string) { return creds(t, " \n", true) }, "empty"},
		"the placeholder":       {func(t *testing.T) (string, string) { return creds(t, proxy.Placeholder, true) }, "placeholder"},
		"no ca-key":             {func(t *testing.T) (string, string) { return creds(t, "sk-ant-api03-REAL", false) }, "ca-key"},
	}
	for name, tc := range cases {
		dir, cert := tc.dir(t)
		_, err := loadProxySettings(validFlags(cert), dir)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want one mentioning %q", name, err, tc.wantErr)
		}
		if err != nil && strings.Contains(err.Error(), "sk-ant-api03-REAL") {
			t.Errorf("%s: the error repeats the key: %v", name, err)
		}
	}
}

func TestProxyCommandStaysHidden(t *testing.T) {
	for _, c := range NewRootCommand().Commands() {
		if c.Name() == "proxy" && !c.Hidden {
			t.Error("the proxy command shows in help")
		}
	}
}
