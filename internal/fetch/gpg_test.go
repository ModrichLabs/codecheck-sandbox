package fetch

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

const fixtureSums = "1111111111111111111111111111111111111111111111111111111111111111 *other.img\n" +
	"2222222222222222222222222222222222222222222222222222222222222222 *disk.squashfs\n"

// testKey makes a signing key and returns it armoured, with its fingerprint.
func testKey(t *testing.T) (*openpgp.Entity, []byte, string) {
	t.Helper()
	e, err := openpgp.NewEntity("Test Signer", "", "signer@example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, _ := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return e, buf.Bytes(), strings.ToUpper(fprHex(e.PrimaryKey.Fingerprint))
}

func fprHex(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexd[c>>4], hexd[c&15])
	}
	return string(out)
}

func sign(t *testing.T, e *openpgp.Entity, data string) []byte {
	t.Helper()
	var sig bytes.Buffer
	if err := openpgp.DetachSign(&sig, e, strings.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	return sig.Bytes()
}

func TestVerifySumsGood(t *testing.T) {
	e, key, fpr := testKey(t)
	if err := VerifySums([]byte(fixtureSums), sign(t, e, fixtureSums), key, fpr); err != nil {
		t.Errorf("VerifySums: %v", err)
	}
}

func TestVerifySumsTampered(t *testing.T) {
	e, key, fpr := testKey(t)
	sig := sign(t, e, fixtureSums)
	tampered := strings.Replace(fixtureSums, "2222", "3333", 1)
	if err := VerifySums([]byte(tampered), sig, key, fpr); !errors.Is(err, ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

func TestVerifySumsWrongKey(t *testing.T) {
	signer, _, _ := testKey(t)
	_, otherKey, otherFpr := testKey(t)
	if err := VerifySums([]byte(fixtureSums), sign(t, signer, fixtureSums), otherKey, otherFpr); !errors.Is(err, ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature", err)
	}
}

func TestVerifySumsKeyFingerprintMustMatchThePin(t *testing.T) {
	e, key, _ := testKey(t)
	err := VerifySums([]byte(fixtureSums), sign(t, e, fixtureSums), key, strings.Repeat("A", 40))
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("err = %v, want ErrBadSignature for a key that differs from the pin", err)
	}
}

func TestLookupSum(t *testing.T) {
	got, err := LookupSum([]byte(fixtureSums), "disk.squashfs")
	if err != nil || got != strings.Repeat("2", 64) {
		t.Errorf("LookupSum = %q, %v", got, err)
	}
	if _, err := LookupSum([]byte(fixtureSums), "disk"); err == nil {
		t.Error("a name prefix matched")
	}
	dup := fixtureSums + strings.Repeat("4", 64) + "  disk.squashfs\n"
	if _, err := LookupSum([]byte(dup), "disk.squashfs"); err == nil {
		t.Error("two lines for one file got accepted")
	}
	if _, err := LookupSum([]byte("zz *disk.squashfs\n"), "disk.squashfs"); err == nil {
		t.Error("a malformed hash got accepted")
	}
}

func TestEmbeddedKeyMatchesThePin(t *testing.T) {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(assets.UbuntuCloudImageKey))
	if err != nil || len(ring) != 1 {
		t.Fatalf("embedded key: %d entities, %v", len(ring), err)
	}
	if got := strings.ToUpper(fprHex(ring[0].PrimaryKey.Fingerprint)); got != pins.Ubuntu.KeyFingerprint {
		t.Errorf("embedded key %s, pin %s", got, pins.Ubuntu.KeyFingerprint)
	}
}

// TestRealUbuntuSums verifies Ubuntu's own published files for the pinned
// release, so the code works on the real signature format, not only on test
// keys.
func TestRealUbuntuSums(t *testing.T) {
	sums, err := os.ReadFile("testdata/ubuntu/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := os.ReadFile("testdata/ubuntu/SHA256SUMS.gpg")
	got, err := VerifiedUbuntuSum(sums, sig)
	if err != nil {
		t.Fatalf("VerifiedUbuntuSum: %v", err)
	}
	if got != pins.Ubuntu.SquashFSSHA256 {
		t.Errorf("signed squashfs hash %s, pin %s", got, pins.Ubuntu.SquashFSSHA256)
	}
	tampered := bytes.Replace(sums, []byte("00b9709d"), []byte("ffb9709d"), 1)
	if _, err := VerifiedUbuntuSum(tampered, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered real sums: err = %v, want ErrBadSignature", err)
	}
}
