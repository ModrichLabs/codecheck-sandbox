package fetch

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
)

// ErrBadSignature reports a checksum list whose signature fails, or a key
// that differs from the pinned one.
var ErrBadSignature = errors.New("signature check failed")

// VerifySums checks the detached signature sig over sums with armouredKey.
// The key must hold exactly one key, whose fingerprint equals
// wantFingerprint, so a swapped key file cannot vouch for anything.
func VerifySums(sums, sig, armouredKey []byte, wantFingerprint string) error {
	ring, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(armouredKey))
	if err != nil {
		return fmt.Errorf("%w: reading the key: %v", ErrBadSignature, err)
	}
	if len(ring) != 1 {
		return fmt.Errorf("%w: the key file holds %d keys, want 1", ErrBadSignature, len(ring))
	}
	got := strings.ToUpper(hex.EncodeToString(ring[0].PrimaryKey.Fingerprint))
	if got != strings.ToUpper(wantFingerprint) {
		return fmt.Errorf("%w: key %s differs from the pinned %s", ErrBadSignature, got, wantFingerprint)
	}
	// Ubuntu armours its SHA256SUMS.gpg despite the extension; accept both.
	check := openpgp.CheckDetachedSignature
	if bytes.HasPrefix(bytes.TrimSpace(sig), []byte("-----BEGIN PGP SIGNATURE-----")) {
		check = openpgp.CheckArmoredDetachedSignature
	}
	if _, err := check(ring, bytes.NewReader(sums), bytes.NewReader(sig), nil); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}
	return nil
}

// LookupSum returns the SHA-256 that sums lists for name. sums uses the
// sha256sum format ("<hex> *<name>" or "<hex>  <name>"); exactly one line
// must name the file.
func LookupSum(sums []byte, name string) (string, error) {
	var found []string
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		hash, file, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		file = strings.TrimPrefix(strings.TrimPrefix(file, " "), "*")
		if file == name {
			found = append(found, hash)
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("reading SHA256SUMS: %w", err)
	}
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("SHA256SUMS lists no %s", name)
	case len(found) > 1:
		return "", fmt.Errorf("SHA256SUMS lists %s %d times", name, len(found))
	case !sha256Pattern.MatchString(found[0]):
		return "", fmt.Errorf("SHA256SUMS holds a malformed hash for %s", name)
	}
	return found[0], nil
}

// VerifiedUbuntuSum checks Ubuntu's SHA256SUMS with the embedded key and
// returns the image's hash from it, which must also equal the pin. Every
// value it checks against comes from pin: key fingerprint, file name, hash.
func VerifiedUbuntuSum(sums, sig []byte, pin pins.UbuntuPin) (string, error) {
	if err := VerifySums(sums, sig, assets.UbuntuCloudImageKey, pin.KeyFingerprint); err != nil {
		return "", err
	}
	name := path.Base(pin.SquashFSURL)
	hash, err := LookupSum(sums, name)
	if err != nil {
		return "", err
	}
	if hash != pin.SquashFSSHA256 {
		return "", fmt.Errorf("%w: signed list gives %s for %s, the pin says %s", ErrHashMismatch, hash, name, pin.SquashFSSHA256)
	}
	return hash, nil
}
