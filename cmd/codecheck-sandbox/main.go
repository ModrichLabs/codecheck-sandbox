// Command codecheck-sandbox sets up, checks and runs CodeCheck reviews inside
// jailed Firecracker microVMs.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with args and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "%s %s\n", buildinfo.Name, buildinfo.Version)
		return 0
	}
	fmt.Fprintf(stderr, "%s: not implemented\n", buildinfo.Name)
	return 2
}
