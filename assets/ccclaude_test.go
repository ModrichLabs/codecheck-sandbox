package assets

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func readGuest(t *testing.T, name string) string {
	t.Helper()
	b, err := Guest.ReadFile("guest/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// runCCClaude runs cc-claude with its fixed paths pointed at temp folders and
// a stand-in claude that prints its arguments and environment.
func runCCClaude(t *testing.T, env ...string) (args []string, childEnv map[string]string, err error) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "work/src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "claude")
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$CC_TEST_OUT/args\"\nenv > \"$CC_TEST_OUT/env\"\npwd > \"$CC_TEST_OUT/pwd\"\n"
	if err := os.WriteFile(fake, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script := readGuest(t, "cc-claude")
	script = strings.ReplaceAll(script, "/work/", dir+"/work/")
	script = strings.ReplaceAll(script, "/usr/local/bin/claude", fake)
	path := filepath.Join(dir, "cc-claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", path)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin", "HOME=" + dir, "CC_TEST_OUT=" + dir}, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, &exec.ExitError{ProcessState: cmd.ProcessState, Stderr: out}
	}
	a, _ := os.ReadFile(filepath.Join(dir, "args"))
	args = strings.Split(strings.TrimSuffix(string(a), "\n"), "\n")
	e, _ := os.ReadFile(filepath.Join(dir, "env"))
	childEnv = map[string]string{}
	for _, l := range strings.Split(string(e), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			childEnv[k] = v
		}
	}
	if p, _ := os.ReadFile(filepath.Join(dir, "pwd")); strings.TrimSpace(string(p)) != src {
		t.Errorf("claude ran in %q, want %q", strings.TrimSpace(string(p)), src)
	}
	return args, childEnv, nil
}

func flagValue(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestCCClaudeFullReview(t *testing.T) {
	args, env, err := runCCClaude(t, "CC_MAX_BUDGET_USD=25", "CC_SCOPE=full", "CC_RUN_ID=r1")
	if err != nil {
		t.Fatalf("cc-claude: %v: %s", err, err.(*exec.ExitError).Stderr)
	}
	// The prompt comes straight after -p: --allowedTools takes several
	// values and would swallow a trailing prompt.
	if len(args) < 2 || args[0] != "-p" || !strings.Contains(args[1], "security review") {
		t.Fatalf("args start %q, want -p and the prompt", args[:min(2, len(args))])
	}
	for flag, want := range map[string]string{
		"--tools":           "Read,Grep,Glob,Agent,Skill",
		"--allowedTools":    "Read,Grep,Glob,Agent,Skill",
		"--plugin-dir":      "/opt/codecheck/plugin",
		"--permission-mode": "dontAsk",
		"--max-budget-usd":  "25",
		"--output-format":   "stream-json",
	} {
		if got, ok := flagValue(args, flag); !ok || got != want {
			t.Errorf("%s = %q, want %q", flag, got, want)
		}
	}
	for _, flag := range []string{"--restricted", "--strict-mcp-config", "--no-session-persistence", "--verbose"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if slices.Contains(args, "--bare") {
		t.Error("cc-claude passes --bare, which removes Grep, Glob, Agent and Skill")
	}
	for k, want := range map[string]string{
		"ANTHROPIC_API_KEY":   "sk-ant-sandbox-placeholder",
		"HTTPS_PROXY":         "http://172.16.0.1:3180",
		"NODE_EXTRA_CA_CERTS": "/usr/local/share/ca-certificates/codecheck-sandbox.crt",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"DISABLE_AUTOUPDATER":                      "1",
		"GIT_CONFIG_NOSYSTEM":                      "1",
		"GIT_CONFIG_GLOBAL":                        "/dev/null",
		"GIT_CONFIG_COUNT":                         "1",
		"GIT_CONFIG_KEY_0":                         "safe.directory",
	} {
		if env[k] != want {
			t.Errorf("env %s = %q, want %q", k, env[k], want)
		}
	}
	if !strings.HasSuffix(env["GIT_CONFIG_VALUE_0"], "/work/src") {
		t.Errorf("safe.directory = %q, want the source folder", env["GIT_CONFIG_VALUE_0"])
	}
	if strings.Contains(args[1], "diff") {
		t.Errorf("a full review's prompt mentions a diff: %q", args[1])
	}
}

func TestCCClaudeDiffReviewReadsTheHostsChangeFiles(t *testing.T) {
	base := strings.Repeat("f", 40)
	args, _, err := runCCClaude(t, "CC_MAX_BUDGET_USD=5", "CC_SCOPE=diff", "CC_BASE="+base)
	if err != nil {
		t.Fatalf("cc-claude: %v: %s", err, err.(*exec.ExitError).Stderr)
	}
	prompt := args[1]
	for _, want := range []string{base, "/work/change/diff.patch", "/work/change/status.txt"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("diff prompt lacks %q: %q", want, prompt)
		}
	}
	// Claude has no shell in the guest, so no tool list may grow for diffs.
	if got, _ := flagValue(args, "--tools"); got != "Read,Grep,Glob,Agent,Skill" {
		t.Errorf("--tools = %q in diff mode", got)
	}
}

// --cwe and --prompt only add words to the request; the user's text stays
// one argument, quoted, and never runs.
func TestCCClaudeFocus(t *testing.T) {
	text := `Review ./routes for injection; $(touch /tmp/pwned) "quotes" 'and' \backslashes`
	args, env, err := runCCClaude(t, "CC_MAX_BUDGET_USD=25", "CC_SCOPE=full", "CC_CWES=89,79", "CC_PROMPT="+text)
	if err != nil {
		t.Fatalf("cc-claude: %v: %s", err, err.(*exec.ExitError).Stderr)
	}
	if _, set := env["LC_ALL"]; set {
		t.Error("the prompt check's LC_ALL leaked into Claude Code's environment")
	}
	prompt := args[1]
	for _, want := range []string{"Run a security review of", "Hunt CWE-89 and CWE-79 first", text} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Error("the prompt's text ran as a command")
	}
	one, _, _ := runCCClaude(t, "CC_MAX_BUDGET_USD=25", "CC_SCOPE=full", "CC_CWES=22")
	if !strings.Contains(one[1], "Hunt CWE-22 first") || strings.Contains(one[1], "user adds") {
		t.Errorf("one CWE, no prompt:\n%s", one[1])
	}
	plain, _, _ := runCCClaude(t, "CC_MAX_BUDGET_USD=25", "CC_SCOPE=full")
	if strings.Contains(plain[1], "CWE") || strings.Contains(plain[1], "user adds") {
		t.Errorf("an unfocused review mentions a focus:\n%s", plain[1])
	}
}

func TestCCClaudeRefusesBadFocus(t *testing.T) {
	base := []string{"CC_MAX_BUDGET_USD=5", "CC_SCOPE=full"}
	for name, extra := range map[string]string{
		"cwe with a flag":  "CC_CWES=89 --bare",
		"cwe letters":      "CC_CWES=89,xss",
		"cwe too long":     "CC_CWES=123456",
		"too many cwes":    "CC_CWES=1,2,3,4,5,6,7,8,9,10,11",
		"empty cwes":       "CC_CWES=",
		"prompt newline":   "CC_PROMPT=one\ntwo",
		"prompt escape":    "CC_PROMPT=clear\x1b[2J",
		"prompt bidi":      "CC_PROMPT=abc\xe2\x80\xaedef",
		"prompt c1":        "CC_PROMPT=abc\xc2\x85def",
		"prompt too long":  "CC_PROMPT=" + strings.Repeat("x", 1001),
		"empty prompt set": "CC_PROMPT=",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := runCCClaude(t, append(base, extra)...); err == nil {
				t.Error("cc-claude ran claude anyway")
			}
		})
	}
}

func TestCCClaudeRefusesBadSettings(t *testing.T) {
	for name, env := range map[string][]string{
		"no budget":          {"CC_SCOPE=full"},
		"budget with a flag": {"CC_MAX_BUDGET_USD=5 --bare", "CC_SCOPE=full"},
		"unknown scope":      {"CC_MAX_BUDGET_USD=5", "CC_SCOPE=everything"},
		"diff no base":       {"CC_MAX_BUDGET_USD=5", "CC_SCOPE=diff"},
		"diff bad base":      {"CC_MAX_BUDGET_USD=5", "CC_SCOPE=diff", "CC_BASE=HEAD~1"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := runCCClaude(t, env...); err == nil {
				t.Error("cc-claude ran claude anyway")
			}
		})
	}
}

// The spec's flag contract, checked on the text too, so no code path (or a
// later edit) can slip --bare in or drop a guard.
func TestCCClaudeTextContract(t *testing.T) {
	s := readGuest(t, "cc-claude")
	for _, want := range []string{"--restricted", "--strict-mcp-config", "--tools Read,Grep,Glob,Agent,Skill"} {
		if !strings.Contains(s, want) {
			t.Errorf("cc-claude lacks %q", want)
		}
	}
	if regexp.MustCompile(`--bare\b`).MatchString(s) {
		t.Error("cc-claude mentions --bare")
	}
	if regexp.MustCompile(`\bBash\b`).MatchString(s) {
		t.Error("cc-claude grants Bash")
	}
}
