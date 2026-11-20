// Package hostnet sets up the host side of the sandbox network: the jail
// user, the tap device, the nftables rules and allowances in other firewall
// managers.
package hostnet

import (
	"bytes"
	"fmt"
	"net/netip"
	"regexp"
	"text/template"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// Config holds every value the host files contain.
type Config struct {
	Tap       string
	HostIP    string
	GuestIP   string
	PrefixLen int
	ProxyPort int
	JailUser  string
	Table     string

	UnitPath   string
	RulesPath  string
	NMPath     string
	HashPath   string // the expected rule hash, for cc-launch
	JailerBase string // the jailer's chroot base

	ProxyUnitPath string
	ProxyBinary   string // the root-owned copy of the CLI the proxy runs
	APIKeyPath    string
	CACertPath    string
	CAKeyPath     string
}

// DefaultConfig returns the footprint from spec §3.
func DefaultConfig() Config {
	return Config{
		Tap:        "cc-tap0",
		HostIP:     "172.16.0.1",
		GuestIP:    "172.16.0.2",
		PrefixLen:  30,
		ProxyPort:  3180,
		JailUser:   "fcjail",
		Table:      "codecheck",
		UnitPath:   "/etc/systemd/system/codecheck-net.service",
		RulesPath:  "/etc/codecheck-sandbox/rules.nft",
		NMPath:     "/etc/NetworkManager/conf.d/codecheck-sandbox.conf",
		HashPath:   "/var/lib/codecheck-sandbox/rules.sha256",
		JailerBase: "/srv/jailer",

		ProxyUnitPath: "/etc/systemd/system/codecheck-proxy.service",
		ProxyBinary:   "/usr/local/lib/codecheck-sandbox/bin/codecheck-sandbox",
		APIKeyPath:    "/etc/codecheck-sandbox/api-key",
		CACertPath:    "/etc/codecheck-sandbox/ca/ca.crt",
		CAKeyPath:     "/etc/codecheck-sandbox/ca/ca.key",
	}
}

var (
	tapPattern   = regexp.MustCompile(`^cc-tap[0-9]{1,3}$`)
	userPattern  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	tablePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// Validate refuses any value that could break out of the templates or
// widen the rules.
func (c Config) Validate() error {
	if !tapPattern.MatchString(c.Tap) {
		return fmt.Errorf("tap %q: want cc-tap and a number", c.Tap)
	}
	host, err1 := netip.ParseAddr(c.HostIP)
	guest, err2 := netip.ParseAddr(c.GuestIP)
	if err1 != nil || err2 != nil || !host.Is4() || !guest.Is4() {
		return fmt.Errorf("addresses %q and %q: want IPv4", c.HostIP, c.GuestIP)
	}
	prefix := netip.PrefixFrom(host, c.PrefixLen)
	if c.PrefixLen < 24 || c.PrefixLen > 30 || !prefix.Masked().Contains(guest) || host == guest {
		return fmt.Errorf("guest %s must sit beside host %s in a /24 to /30", c.GuestIP, c.HostIP)
	}
	network := prefix.Masked().Addr()
	broadcast := lastAddr(prefix.Masked())
	for _, a := range []netip.Addr{host, guest} {
		if a == network || a == broadcast {
			return fmt.Errorf("%s: the subnet's network or broadcast address can't serve a host", a)
		}
	}
	if c.ProxyPort < 1024 || c.ProxyPort > 65535 {
		return fmt.Errorf("proxy port %d: want 1024 to 65535", c.ProxyPort)
	}
	if !userPattern.MatchString(c.JailUser) || c.JailUser == "root" {
		return fmt.Errorf("jail user %q: want an unprivileged user name", c.JailUser)
	}
	if !tablePattern.MatchString(c.Table) {
		return fmt.Errorf("table %q: want a plain name", c.Table)
	}
	// Every path must sit inside the footprint the manifest allows, which
	// also keeps them free of spaces and other template-breaking text.
	for _, p := range []string{c.UnitPath, c.RulesPath, c.NMPath, c.HashPath, c.JailerBase,
		c.ProxyUnitPath, c.ProxyBinary, c.APIKeyPath, c.CACertPath, c.CAKeyPath} {
		if err := step.CheckPath(p); err != nil {
			return err
		}
	}
	return nil
}

// Render fills the four host templates from c.
func Render(c Config) ([]step.PlannedFile, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	out := []step.PlannedFile{}
	for _, f := range []struct{ tmpl, path string }{
		{"host/codecheck-net.service.tmpl", c.UnitPath},
		{"host/rules.nft.tmpl", c.RulesPath},
		{"host/nm-unmanaged.conf.tmpl", c.NMPath},
		{"host/codecheck-proxy.service.tmpl", c.ProxyUnitPath},
	} {
		t, err := template.New(f.tmpl).Option("missingkey=error").ParseFS(assets.Host, f.tmpl)
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, pathBase(f.tmpl), c); err != nil {
			return nil, fmt.Errorf("rendering %s: %w", f.tmpl, err)
		}
		out = append(out, step.PlannedFile{Path: f.path, Mode: 0o644, Content: buf.Bytes()})
	}
	return out, nil
}

// lastAddr returns an IPv4 prefix's broadcast address.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	host := 32 - p.Bits()
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	n |= 1<<host - 1
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

func pathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
