package guestinit

import (
	"slices"
	"strings"
	"testing"
)

func focused(cwes []int, prompt string) Config {
	return Config{Mode: ModeReview, RunID: "r1", MaxBudgetUSD: "25", Scope: ScopeFull, CWEs: cwes, Prompt: prompt}
}

func TestFocusRoundTrips(t *testing.T) {
	prompt := "Review ./routes for injection flaws; treat the admin API as internet-facing. Ünïcode ✓ and = signs too."
	c := focused([]int{89, 79, 22}, prompt)
	b, err := Encode(c)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !slices.Equal(back.CWEs, c.CWEs) || back.Prompt != prompt {
		t.Errorf("round trip = %+v", back)
	}
	// The longest prompt still fits the block with every other key.
	long := focused([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, strings.Repeat("é", MaxPrompt/2))
	long.Scope, long.Base = ScopeDiff, strings.Repeat("a", 64)
	if b, err := Encode(long); err != nil {
		t.Fatalf("Encode at the limits: %v", err)
	} else if back, err := Parse(b); err != nil || back.Prompt != long.Prompt {
		t.Errorf("Parse at the limits: %v", err)
	}
}

func TestFocusRefusals(t *testing.T) {
	for name, c := range map[string]Config{
		"newline":         focused(nil, "line one\nline two"),
		"control byte":    focused(nil, "clear\x1b[2J"),
		"tab":             focused(nil, "a\tb"),
		"bidi override":   focused(nil, "abc\u202edef"),
		"invalid utf-8":   focused(nil, "bad \xff byte"),
		"too long":        focused(nil, strings.Repeat("x", MaxPrompt+1)),
		"cwe zero":        focused([]int{0}, ""),
		"cwe too big":     focused([]int{100000}, ""),
		"too many cwes":   focused([]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, ""),
		"repeated cwe":    focused([]int{89, 89}, ""),
		"cwe in selftest": {Mode: ModeSelftest, RunID: "r1", CWEs: []int{89}},
		"prompt in build": {Mode: ModeBuild, RunID: "r1", Prompt: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Encode(c); err == nil {
				t.Error("Encode accepted it")
			}
		})
	}
	// Parse refuses what a tampered block carries, too.
	for name, line := range map[string]string{
		"cwe letters":  "cwes=89,abc\n",
		"cwe spaces":   "cwes=89, 79\n",
		"empty cwes":   "cwes=\n",
		"empty prompt": "prompt=\n",
	} {
		t.Run("parse "+name, func(t *testing.T) {
			b := make([]byte, BlockSize)
			copy(b, "mode=review\nrun_id=r1\nmax_budget_usd=25\nscope=full\n"+line)
			if _, err := Parse(b); err == nil {
				t.Error("Parse accepted it")
			}
		})
	}
}

func TestJobEnvCarriesTheFocus(t *testing.T) {
	env := jobEnv(focused([]int{89, 79}, "Review ./routes"))
	if !slices.Contains(env, "CC_CWES=89,79") || !slices.Contains(env, "CC_PROMPT=Review ./routes") {
		t.Errorf("env = %v", env)
	}
	plain := jobEnv(focused(nil, ""))
	for _, kv := range plain {
		if strings.HasPrefix(kv, "CC_CWES=") || strings.HasPrefix(kv, "CC_PROMPT=") {
			t.Errorf("an unfocused review carries %s", kv)
		}
	}
}
