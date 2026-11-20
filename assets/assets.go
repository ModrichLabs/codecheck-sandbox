// Package assets embeds the files the binary carries with it, such as the
// signing keys setup trusts.
package assets

import _ "embed"

// UbuntuCloudImageKey holds the "UEC Image Automatic Signing Key", which
// signs Ubuntu's cloud image SHA256SUMS. Its fingerprint matches
// pins.Ubuntu.KeyFingerprint; a test checks that.
//
//go:embed keys/ubuntu-cloudimage.asc
var UbuntuCloudImageKey []byte
