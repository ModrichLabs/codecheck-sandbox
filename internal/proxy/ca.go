package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

// APIHost names the one host the proxy serves, and the only name the
// sandbox CA may vouch for.
const APIHost = "api.anthropic.com"

const (
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 24 * time.Hour
	leafRenew    = 2 * time.Hour // renew a leaf this long before it expires
	clockSkew    = time.Hour     // leaves start this early, for a guest clock behind the host's
)

// CA holds the sandbox CA. Its name constraint permits api.anthropic.com
// alone and excludes every IP address, and it allows no intermediate CAs, so
// even a leaked key cannot vouch for anything else.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// NewCA generates a fresh CA (ECDSA P-256, valid ten years from now) and
// returns its certificate and key as PEM.
func NewCA(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	_, all4, _ := net.ParseCIDR("0.0.0.0/0")
	_, all6, _ := net.ParseCIDR("::/0")
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: "codecheck-sandbox CA", Organization: []string{"codecheck-sandbox"}},
		NotBefore:                   now.Add(-clockSkew),
		NotAfter:                    now.Add(caLifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{APIHost},
		ExcludedIPRanges:            []*net.IPNet{all4, all6},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), nil
}

// LoadCA parses a CA from PEM. It refuses a key that doesn't match the
// certificate, and a certificate that lost its name constraint.
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || cb.Type != "CERTIFICATE" || kb == nil || kb.Type != "EC PRIVATE KEY" {
		return nil, errors.New("CA: want a PEM certificate and a PEM EC private key")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("CA: the key does not match the certificate")
	}
	if !cert.IsCA || !cert.PermittedDNSDomainsCritical || len(cert.PermittedDNSDomains) != 1 || cert.PermittedDNSDomains[0] != APIHost {
		return nil, fmt.Errorf("CA: want a CA with a critical name constraint for %s only", APIHost)
	}
	return &CA{Cert: cert, Key: key}, nil
}

// Leaf mints a fresh certificate for api.anthropic.com, valid for a day.
func (ca *CA) Leaf(now time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: APIHost},
		DNSNames:     []string{APIHost},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(leafLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// LeafCache serves the API leaf to TLS handshakes, minting a new one as the
// current one nears its expiry. The leaf lives only in memory.
type LeafCache struct {
	ca   *CA
	now  func() time.Time
	mu   sync.Mutex
	leaf *tls.Certificate
}

// NewLeafCache serves leaves from ca; now defaults to time.Now.
func NewLeafCache(ca *CA, now func() time.Time) *LeafCache {
	if now == nil {
		now = time.Now
	}
	return &LeafCache{ca: ca, now: now}
}

// GetCertificate suits tls.Config.GetCertificate. It refuses any name but
// the API host.
func (c *LeafCache) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName != APIHost {
		return nil, fmt.Errorf("no certificate for %q", hello.ServerName)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.leaf == nil || now.After(c.leaf.Leaf.NotAfter.Add(-leafRenew)) {
		leaf, err := c.ca.Leaf(now)
		if err != nil {
			return nil, err
		}
		c.leaf = leaf
	}
	return c.leaf, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n.Add(n, big.NewInt(1))
}
