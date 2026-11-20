package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func testCA(t *testing.T) *CA {
	t.Helper()
	certPEM, keyPEM, err := NewCA(now)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	ca, err := LoadCA(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	return ca
}

func verify(ca *CA, leafDER []byte, name string) error {
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name, CurrentTime: now.Add(time.Hour)})
	return err
}

// rogueLeaf signs a leaf for any names with the CA key directly, as an
// attacker holding the key could.
func rogueLeaf(t *testing.T, ca *CA, dns []string, ips []net.IP) []byte {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: "rogue"},
		DNSNames: dns, IPAddresses: ips,
		NotBefore: now, NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &k.PublicKey, ca.Key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestCAShape(t *testing.T) {
	ca := testCA(t)
	c := ca.Cert
	if !c.IsCA || !c.MaxPathLenZero || c.MaxPathLen != 0 {
		t.Error("the CA must allow no intermediate CAs")
	}
	if !c.PermittedDNSDomainsCritical || len(c.PermittedDNSDomains) != 1 || c.PermittedDNSDomains[0] != "api.anthropic.com" {
		t.Errorf("name constraint = %v (critical %v)", c.PermittedDNSDomains, c.PermittedDNSDomainsCritical)
	}
	if len(c.ExcludedIPRanges) != 2 {
		t.Errorf("excluded IP ranges = %v, want all of IPv4 and IPv6", c.ExcludedIPRanges)
	}
	if years := c.NotAfter.Sub(c.NotBefore).Hours() / 24 / 365; years < 9.9 || years > 10.1 {
		t.Errorf("validity %.1f years, want 10", years)
	}
	if ca.Key.Curve != elliptic.P256() {
		t.Error("want an ECDSA P-256 key")
	}
}

func TestLeafForTheAPIVerifies(t *testing.T) {
	ca := testCA(t)
	leaf, err := ca.Leaf(now)
	if err != nil {
		t.Fatalf("Leaf: %v", err)
	}
	if err := verify(ca, leaf.Certificate[0], "api.anthropic.com"); err != nil {
		t.Errorf("the API leaf fails: %v", err)
	}
	if leaf.Leaf.NotAfter.Sub(now) > 25*time.Hour {
		t.Errorf("leaf lives until %v; want about a day", leaf.Leaf.NotAfter)
	}
}

func TestConstraintsStopLeavesForOtherNames(t *testing.T) {
	ca := testCA(t)
	cases := map[string][]byte{
		"evil.example":               rogueLeaf(t, ca, []string{"evil.example"}, nil),
		"sub.api.anthropic.com.evil": rogueLeaf(t, ca, []string{"api.anthropic.com.evil"}, nil),
		"an IP address":              rogueLeaf(t, ca, nil, []net.IP{net.ParseIP("93.184.216.34")}),
		"an IPv6 address":            rogueLeaf(t, ca, nil, []net.IP{net.ParseIP("2606:2800::1")}),
	}
	for name, der := range cases {
		names := []string{"evil.example", "api.anthropic.com.evil", "93.184.216.34", "2606:2800::1"}
		for _, n := range names {
			if verify(ca, der, n) == nil {
				t.Errorf("%s: a rogue leaf verified for %s", name, n)
			}
		}
	}
}

func TestLoadCARefusesMismatchesAndMissingConstraints(t *testing.T) {
	certPEM, _, _ := NewCA(now)
	_, otherKey, _ := NewCA(now)
	if _, err := LoadCA(certPEM, otherKey); err == nil {
		t.Error("LoadCA accepted a key that doesn't match the certificate")
	}

	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true,
		NotBefore: now, NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	kder, _ := x509.MarshalECPrivateKey(k)
	unconstrained := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
	if _, err := LoadCA(unconstrained, key); err == nil || !strings.Contains(err.Error(), "constraint") {
		t.Errorf("LoadCA accepted a CA without the name constraint: %v", err)
	}
}

func TestLeafCacheRenews(t *testing.T) {
	ca := testCA(t)
	clock := now
	lc := NewLeafCache(ca, func() time.Time { return clock })
	a, err := lc.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.anthropic.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	b, _ := lc.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.anthropic.com"})
	if a != b {
		t.Error("a fresh leaf got replaced")
	}
	clock = now.Add(23 * time.Hour)
	c, _ := lc.GetCertificate(&tls.ClientHelloInfo{ServerName: "api.anthropic.com"})
	if c == a {
		t.Error("a leaf near its expiry got reused")
	}
	if _, err := lc.GetCertificate(&tls.ClientHelloInfo{ServerName: "evil.example"}); err == nil {
		t.Error("the cache served a leaf for another name")
	}
}
