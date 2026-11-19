// Package buildinfo holds the program name and version. Release builds may
// replace Version with -ldflags "-X".
package buildinfo

// Name names the command in output and help.
const Name = "codecheck-sandbox"

// Version follows Semantic Versioning; "-dev" marks an unreleased build.
var Version = "0.1.0-dev"
