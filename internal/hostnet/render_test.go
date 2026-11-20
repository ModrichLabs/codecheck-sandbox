package hostnet

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestRenderGolden(t *testing.T) {
	files, err := Render(DefaultConfig())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("Render gave %d files, want 4", len(files))
	}
	for _, f := range files {
		golden := filepath.Join("testdata", "golden", filepath.Base(f.Path))
		if *update {
			if err := os.WriteFile(golden, f.Content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("reading %s: %v", golden, err)
		}
		if string(f.Content) != string(want) {
			t.Errorf("%s =\n%s\nwant\n%s", f.Path, f.Content, want)
		}
		if f.Mode != 0o644 {
			t.Errorf("%s mode %v, want 0644", f.Path, f.Mode)
		}
	}
}

func TestRenderPaths(t *testing.T) {
	files, _ := Render(DefaultConfig())
	want := map[string]bool{
		"/etc/systemd/system/codecheck-net.service":         true,
		"/etc/codecheck-sandbox/rules.nft":                  true,
		"/etc/NetworkManager/conf.d/codecheck-sandbox.conf": true,
		"/etc/systemd/system/codecheck-proxy.service":       true,
	}
	for _, f := range files {
		if !want[f.Path] {
			t.Errorf("unexpected path %s", f.Path)
		}
	}
}

func TestUnitLoadsRulesBeforeTheTapComesUp(t *testing.T) {
	files, _ := Render(DefaultConfig())
	var unit string
	for _, f := range files {
		if strings.HasSuffix(f.Path, "codecheck-net.service") {
			unit = string(f.Content)
		}
	}
	rules := strings.Index(unit, "ExecStart=nft -f")
	tap := strings.Index(unit, "ExecStart=ip tuntap add")
	up := strings.Index(unit, "ExecStart=ip link set dev cc-tap0 up")
	if rules < 0 || tap < 0 || up < 0 || !(rules < tap && tap < up) {
		t.Errorf("the unit must load the rules, then add the tap, then bring it up:\n%s", unit)
	}
}

func TestProxyUnitHardening(t *testing.T) {
	files, _ := Render(DefaultConfig())
	var unit string
	for _, f := range files {
		if strings.HasSuffix(f.Path, "codecheck-proxy.service") {
			unit = string(f.Content)
		}
	}
	for _, want := range []string{
		"BindsTo=codecheck-net.service", "After=codecheck-net.service",
		"DynamicUser=yes",
		"LoadCredential=api-key:/etc/codecheck-sandbox/api-key",
		"LoadCredential=ca-key:/etc/codecheck-sandbox/ca/ca.key",
		"LogsDirectory=codecheck-sandbox",
		"ProtectSystem=strict", "PrivateTmp=yes", "NoNewPrivileges=yes",
		"RestrictAddressFamilies=AF_INET AF_INET6",
		"CapabilityBoundingSet=\n",
		"ExecStart=/usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox proxy --listen 172.16.0.1:3180 --client 172.16.0.2",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("proxy unit lacks %q", want)
		}
	}
}

func TestConfigRefusesInjection(t *testing.T) {
	bad := []func(*Config){
		func(c *Config) { c.Tap = "cc-tap0\nExecStart=/bin/sh" },
		func(c *Config) { c.Tap = "eth0" },
		func(c *Config) { c.HostIP = "172.16.0.1; drop" },
		func(c *Config) { c.GuestIP = "fe80::1" },
		func(c *Config) { c.JailUser = "root" },
		func(c *Config) { c.ProxyPort = 0 },
		func(c *Config) { c.Table = "codecheck }" },
	}
	for i, mutate := range bad {
		c := DefaultConfig()
		mutate(&c)
		if _, err := Render(c); err == nil {
			t.Errorf("case %d: Render accepted %+v", i, c)
		}
	}
}
