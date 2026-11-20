package guestinit

import (
	"bytes"
	"strings"
	"testing"
)

// block pads text with NUL bytes to the fixed block size.
func block(text string) []byte {
	b := make([]byte, BlockSize)
	copy(b, text)
	return b
}

func TestParseValidBlocks(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Config
	}{
		{
			name: "full review",
			text: "mode=review\nrun_id=cc-20261121-101500\nmax_budget_usd=25\nscope=full\n",
			want: Config{Mode: ModeReview, RunID: "cc-20261121-101500", MaxBudgetUSD: "25", Scope: ScopeFull},
		},
		{
			name: "diff review",
			text: "mode=review\nrun_id=r1\nmax_budget_usd=2.50\nscope=diff\nbase=" + strings.Repeat("a", 40) + "\n",
			want: Config{Mode: ModeReview, RunID: "r1", MaxBudgetUSD: "2.50", Scope: ScopeDiff, Base: strings.Repeat("a", 40)},
		},
		{
			name: "selftest with extra ports",
			text: "mode=selftest\nrun_id=st1\nprobe_ports=8080,5432\n",
			want: Config{Mode: ModeSelftest, RunID: "st1", ProbePorts: []int{8080, 5432}},
		},
		{
			name: "build",
			text: "mode=build\nrun_id=b1\n",
			want: Config{Mode: ModeBuild, RunID: "b1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(block(tc.text))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Mode != tc.want.Mode || got.RunID != tc.want.RunID || got.MaxBudgetUSD != tc.want.MaxBudgetUSD ||
				got.Scope != tc.want.Scope || got.Base != tc.want.Base || !equalInts(got.ProbePorts, tc.want.ProbePorts) {
				t.Errorf("Parse = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRefusesBadBlocks(t *testing.T) {
	sha := strings.Repeat("b", 40)
	cases := map[string][]byte{
		"missing mode":         block("run_id=r1\n"),
		"unknown mode":         block("mode=shell\nrun_id=r1\n"),
		"unknown key":          block("mode=build\nrun_id=r1\nANTHROPIC_API_KEY=x\n"),
		"duplicate key":        block("mode=build\nmode=review\nrun_id=r1\n"),
		"oversize value":       block("mode=build\nrun_id=" + strings.Repeat("a", 200) + "\n"),
		"no equals sign":       block("mode=build\nrun_id\n"),
		"unterminated line":    block("mode=build\nrun_id=r1"),
		"blank line":           block("mode=build\n\nrun_id=r1\n"),
		"carriage return":      block("mode=build\r\nrun_id=r1\n"),
		"text after padding":   append(block("mode=build\nrun_id=r1\n")[:100], append(make([]byte, BlockSize-101), 'x')...),
		"short block":          []byte("mode=build\nrun_id=r1\n"),
		"long block":           append(block("mode=build\nrun_id=r1\n"), 0),
		"bad run id":           block("mode=build\nrun_id=../etc\n"),
		"missing run id":       block("mode=build\n"),
		"review without scope": block("mode=review\nrun_id=r1\nmax_budget_usd=5\n"),
		"review no budget":     block("mode=review\nrun_id=r1\nscope=full\n"),
		"zero budget":          block("mode=review\nrun_id=r1\nmax_budget_usd=0\nscope=full\n"),
		"budget flag":          block("mode=review\nrun_id=r1\nmax_budget_usd=5 --bare\nscope=full\n"),
		"diff without base":    block("mode=review\nrun_id=r1\nmax_budget_usd=5\nscope=diff\n"),
		"base on full":         block("mode=review\nrun_id=r1\nmax_budget_usd=5\nscope=full\nbase=" + sha + "\n"),
		"short base":           block("mode=review\nrun_id=r1\nmax_budget_usd=5\nscope=diff\nbase=abc123\n"),
		"base option":          block("mode=review\nrun_id=r1\nmax_budget_usd=5\nscope=diff\nbase=--output=/x\n"),
		"bad port":             block("mode=selftest\nrun_id=r1\nprobe_ports=70000\n"),
		"port list junk":       block("mode=selftest\nrun_id=r1\nprobe_ports=80;reboot\n"),
		"ports on review":      block("mode=review\nrun_id=r1\nmax_budget_usd=5\nscope=full\nprobe_ports=80\n"),
		"budget on build":      block("mode=build\nrun_id=r1\nmax_budget_usd=5\n"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := Parse(b); err == nil {
				t.Errorf("Parse accepted the block: %+v", got)
			}
		})
	}
}

func TestEncodeRoundTrips(t *testing.T) {
	in := Config{Mode: ModeReview, RunID: "r9", MaxBudgetUSD: "10", Scope: ScopeDiff, Base: strings.Repeat("c", 64)}
	b, err := Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(b) != BlockSize {
		t.Fatalf("Encode gave %d bytes, want %d", len(b), BlockSize)
	}
	if !bytes.HasPrefix(b, []byte("mode=review\nrun_id=r9\n")) {
		t.Errorf("Encode wrote %q", b[:40])
	}
	out, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(Encode): %v", err)
	}
	if out.Mode != in.Mode || out.RunID != in.RunID || out.MaxBudgetUSD != in.MaxBudgetUSD ||
		out.Scope != in.Scope || out.Base != in.Base {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
}

func TestEncodeRefusesInvalidConfig(t *testing.T) {
	if _, err := Encode(Config{Mode: ModeReview, RunID: "r1"}); err == nil {
		t.Error("Encode accepted a review with no scope or budget")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
