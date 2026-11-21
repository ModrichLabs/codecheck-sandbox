// Package review prepares a CodeCheck review on the host, runs it through
// cc-launch, and turns what comes back into a report: a fresh, hardened
// clone, a read-only code disk, then post-run checks.
package review

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// git runs git on the host with hooks off, only HTTPS (and, for a local
// source, file) transports, and neither system nor global config: nothing in
// the user's setup or the repository under review gets to run code here.
func git(ctx context.Context, r system.Runner, allowFile bool, args ...string) ([]byte, error) {
	file := "never"
	if allowFile {
		file = "always"
	}
	hardened := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.file.allow=" + file,
	}
	res, err := r.Run(ctx, system.Cmd{
		Name:     "git",
		Args:     append(hardened, args...),
		Env:      []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"},
		CleanEnv: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		msg := fmt.Sprintf("git %s: exit %d: %s", strings.Join(args, " "), res.Code, strings.TrimSpace(string(res.Stderr)))
		return nil, errors.New(urlUserinfo.ReplaceAllString(msg, "://"))
	}
	return res.Stdout, nil
}

// urlUserinfo matches the user part of a URL, which can hold a token.
var urlUserinfo = regexp.MustCompile(`://[^/@\s'"]+@`)

// DisplayTarget gives the target fit to show or store: a URL loses any
// user and token; a path stays as it is.
func DisplayTarget(t string) string {
	u, err := url.Parse(t)
	if err != nil || u.Scheme == "" {
		return t
	}
	u.User = nil
	return u.String()
}
