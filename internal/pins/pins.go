// Package pins holds every fixed external reference the tool trusts: release
// versions, checksums and endpoints. Each value below got checked against its
// source on 2026-11-20 (see the M5 notes in the plan).
package pins

// PostHog telemetry (spec §11). The project key only writes events, so it may
// sit in public code. Both stay variables so a fork can point at its own
// project at build time:
//
//	go build -ldflags "-X github.com/ModrichLabs/codecheck-sandbox/internal/pins.PostHogKey=phc_…"
//
// An empty key switches telemetry to the no-op client.
var (
	PostHogKey  = "phc_pR2TVwdPiaMaERt5bD6h77nvDkhq59G7bsvip8aPNyiP"
	PostHogHost = "https://us.i.posthog.com"
)

// FirecrackerPin names the Firecracker and jailer release tarball.
type FirecrackerPin struct {
	Version string
	URL     string
	SHA256  string // matches the release's own .sha256.txt
}

// Firecracker v1.18.0 (spike S1).
var Firecracker = FirecrackerPin{
	Version: "v1.18.0",
	URL:     "https://github.com/firecracker-microvm/firecracker/releases/download/v1.18.0/firecracker-v1.18.0-x86_64.tgz",
	SHA256:  "1ac8eec866dd5eea14efecf6323e22b23eea24054545338a5b34bc5bca6623cd",
}

// KernelPin names the guest kernel.
type KernelPin struct {
	Version string
	URL     string
	SHA256  string
}

// Kernel names Firecracker's CI guest kernel 6.1.155 (spike S1). The URL points
// at Firecracker's CI bucket, whose newest folder stops at v1.15; the SHA-256
// pin decides what gets accepted, wherever the bytes come from.
var Kernel = KernelPin{
	Version: "6.1.155",
	URL:     "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155",
	SHA256:  "e20e46d0c36c55c0d1014eb20576171b3f3d922260d9f792017aeff53af3d4f2",
}

// UbuntuPin names the guest's base image and how to verify it.
type UbuntuPin struct {
	SquashFSURL    string
	SquashFSSHA256 string
	SumsURL        string // SHA256SUMS beside the image
	SumsSigURL     string // its detached signature
	KeyFingerprint string // assets/keys/ubuntu-cloudimage.asc
}

// Ubuntu 24.04 server cloud image (spike S2), from a dated release folder:
// the plain release/ folder moves to each new build, which would break the
// SHA-256 pin.
var Ubuntu = UbuntuPin{
	SquashFSURL:    "https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.squashfs",
	SquashFSSHA256: "00b9709d33192c8cfb987fd9522e9a035c73fd125c5a479b125b5d26ffafec67",
	SumsURL:        "https://cloud-images.ubuntu.com/releases/noble/release-20261017/SHA256SUMS",
	SumsSigURL:     "https://cloud-images.ubuntu.com/releases/noble/release-20261017/SHA256SUMS.gpg",
	KeyFingerprint: "D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81",
}

// ClaudeCodePin names the Claude Code native binary and its npm tarball.
type ClaudeCodePin struct {
	Version      string
	TarballURL   string
	Integrity    string // npm dist.integrity of the tarball
	BinarySHA256 string // package/claude inside it
}

// ClaudeCode 2.1.327 (spike S3).
var ClaudeCode = ClaudeCodePin{
	Version:      "2.1.327",
	TarballURL:   "https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-2.1.327.tgz",
	Integrity:    "sha512-uJ24I+t47cpQjeo31ZrHepsFQ8QUsLjCxmDxLshMjYHEtoxhQdgKc4iuViQDaKpFqn3puR85gwHlLj4e7s+1iQ==",
	BinarySHA256: "343c744baa14bfd38286a7cb90871051f04379956da6c5b803a9e66019a76ac9",
}

// CodeCheckPin names the CodeCheck plugin source.
type CodeCheckPin struct {
	RepoURL string
	Tag     string
	Commit  string
}

// CodeCheck v0.0.1; rebuild --commit may choose another commit later.
var CodeCheck = CodeCheckPin{
	RepoURL: "https://github.com/Commonwealth-Bank-of-Australia/CodeCheck",
	Tag:     "v0.0.1",
	Commit:  "3231a0def630ff7f1529de47af2ea763d23b8217",
}
