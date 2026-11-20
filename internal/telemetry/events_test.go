package telemetry

import (
	"reflect"
	"testing"
	"time"
)

func TestDurationBandEdges(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want DurationBand
	}{
		{0, "<10s"},
		{10*time.Second - 1, "<10s"},
		{10 * time.Second, "10s-1m"},
		{time.Minute - 1, "10s-1m"},
		{time.Minute, "1-5m"},
		{5 * time.Minute, "5-15m"},
		{15 * time.Minute, "15-60m"},
		{time.Hour - 1, "15-60m"},
		{time.Hour, ">=60m"},
		{-time.Second, "<10s"},
	}
	for _, tt := range tests {
		if got := DurationBandOf(tt.d); got != tt.want {
			t.Errorf("DurationBandOf(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestCostBandEdges(t *testing.T) {
	tests := []struct {
		usd  float64
		want CostBand
	}{
		{0, "<$1"},
		{0.99, "<$1"},
		{1, "$1-5"},
		{4.99, "$1-5"},
		{5, "$5-10"},
		{10, "$10-25"},
		{24.99, "$10-25"},
		{25, ">=$25"},
		{1000, ">=$25"},
	}
	for _, tt := range tests {
		if got := CostBandOf(tt.usd); got != tt.want {
			t.Errorf("CostBandOf(%v) = %q, want %q", tt.usd, got, tt.want)
		}
	}
}

func TestSizeBandEdges(t *testing.T) {
	const gb = 1 << 30
	tests := []struct {
		bytes uint64
		want  SizeBand
	}{
		{0, "<8GB"},
		{8*gb - 1, "<8GB"},
		{8 * gb, "8-16GB"},
		{16 * gb, "16-32GB"},
		{32 * gb, "32-64GB"},
		{64*gb - 1, "32-64GB"},
		{64 * gb, ">=64GB"},
	}
	for _, tt := range tests {
		if got := SizeBandOf(tt.bytes); got != tt.want {
			t.Errorf("SizeBandOf(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestCountBandEdges(t *testing.T) {
	tests := []struct {
		n    int
		want CountBand
	}{
		{-1, "0"}, {0, "0"}, {1, "1"}, {2, "2-5"}, {5, "2-5"}, {6, "6-10"}, {10, "6-10"}, {11, ">10"},
	}
	for _, tt := range tests {
		if got := CountBandOf(tt.n); got != tt.want {
			t.Errorf("CountBandOf(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestPropertiesOfAnEvent(t *testing.T) {
	RegisterStepIDs("jail-user")
	e := StepFinished{
		Step:     "jail-user",
		Outcome:  OutcomeFailed,
		Duration: DurationBandOf(3 * time.Second),
	}
	got, err := Properties(e)
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}
	want := map[string]any{"step": "jail-user", "outcome": "failed", "duration": "<10s"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Properties = %v, want %v", got, want)
	}
	if e.EventName() != "step_finished" {
		t.Errorf("EventName = %q", e.EventName())
	}
}

func TestUnknownValuesCollapse(t *testing.T) {
	e := CommandRun{
		Command:    Command("/home/alice/project"),
		Outcome:    Outcome("401 from sk-ant-xyz"),
		Duration:   DurationBand("37.2s"),
		CLIVersion: Version("0.1.0 built by alice@laptop"),
	}
	got, err := Properties(e)
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}
	want := map[string]any{"command": "other", "outcome": "other", "duration": "other", "cli_version": "invalid"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Properties = %v, want %v", got, want)
	}
}

func TestVersionAcceptsRealVersions(t *testing.T) {
	for _, v := range []string{"0.1.0", "0.1.0-dev", "v1.18.0", "2.1.37", "6.17", "24.04", "rolling"} {
		if got := Version(v).value(); got != v {
			t.Errorf("Version(%q) encodes as %v", v, got)
		}
	}
}

func TestStepIDRejectsFreeText(t *testing.T) {
	RegisterStepIDs("nft-rules")
	for _, s := range []string{"/etc/passwd", "Hello World", "sk-ant-api03-abc", "a.b", ""} {
		if got := StepID(s).value(); got != "other" {
			t.Errorf("StepID(%q) encodes as %v, want other", s, got)
		}
	}
	if got := StepID("nft-rules").value(); got != "nft-rules" {
		t.Errorf("StepID(nft-rules) encodes as %v", got)
	}
}
