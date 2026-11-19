// Command codecheck-sandbox sets up, checks and runs CodeCheck reviews inside
// jailed Firecracker microVMs.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/cli"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the CLI with args and returns the process exit code: 0 on
// success, 1 on any error.
func run(args []string, stdout, stderr io.Writer) int {
	root := cli.NewRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", buildinfo.Name, err)
		return 1
	}
	return 0
}
