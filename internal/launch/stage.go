package launch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// StageRun fills a new run folder for cc-launch, from the CLI's side: it
// makes the root-owned folder and installs a root-owned copy of each named
// file from srcDir. root runs commands through the privilege tool.
func StageRun(ctx context.Context, root system.Runner, p Paths, id RunID, srcDir string, names []string) error {
	run := id.Dir(p)
	cmds := []system.Cmd{
		{Name: "install", Args: []string{"-d", "-m", "0755", "-o", "root", "-g", "root", p.RunsDir}},
		{Name: "install", Args: []string{"-d", "-m", "0755", "-o", "root", "-g", "root", run}},
	}
	for _, n := range names {
		if n == "" || n == "." || n == ".." || strings.ContainsRune(n, '/') {
			return fmt.Errorf("run file %q: want a plain name", n)
		}
		cmds = append(cmds, system.Cmd{Name: "install", Args: []string{"-m", "0644", "-o", "root", "-g", "root", "-T",
			filepath.Join(srcDir, n), filepath.Join(run, n)}})
	}
	for _, c := range cmds {
		c.Root = true
		res, err := root.Run(ctx, c)
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("%s %s: exit %d: %s", c.Name, strings.Join(c.Args, " "), res.Code, strings.TrimSpace(string(res.Stderr)))
		}
	}
	return nil
}
