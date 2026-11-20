package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

const id = "cc-20261121-101500-a1b2c3"

// fakeOps records which action main picked.
type fakeOps struct {
	called string
	err    error
}

func (f *fakeOps) launch(_ context.Context, rid launch.RunID, build bool) error {
	f.called = fmt.Sprintf("launch %s build=%v", rid, build)
	return f.err
}

func (f *fakeOps) clean(_ context.Context, rid launch.RunID) error {
	f.called = "clean " + rid.String()
	return f.err
}

func TestArgumentForms(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{id}, "launch " + id + " build=false"},
		{[]string{"build", id}, "launch " + id + " build=true"},
		{[]string{"clean", id}, "clean " + id},
	} {
		f := &fakeOps{}
		if code := run(tc.args, &bytes.Buffer{}, 0, f); code != 0 || f.called != tc.want {
			t.Errorf("%v: code %d, called %q; want 0, %q", tc.args, code, f.called, tc.want)
		}
	}
}

func TestRefusesAnythingElse(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{},
		{"--help"},
		{id, "extra"},
		{"build"},
		{"build", id, "extra"},
		{"review", id},
		{"--config-file=/etc/shadow", id},
		{"../" + id},
		{"build", "--", id},
		{"clean", "../../etc"},
	} {
		f := &fakeOps{}
		var stderr bytes.Buffer
		if code := run(args, &stderr, 0, f); code != ExitUsage || f.called != "" {
			t.Errorf("%q: code %d, called %q; want %d and nothing run", args, code, f.called, ExitUsage)
		}
		if !strings.Contains(stderr.String(), "usage") {
			t.Errorf("%q: stderr %q lacks the usage line", args, stderr.String())
		}
	}
}

func TestRefusesWithoutRoot(t *testing.T) {
	f := &fakeOps{}
	if code := run([]string{id}, &bytes.Buffer{}, 1000, f); code != ExitEnvironment || f.called != "" {
		t.Errorf("as uid 1000: code %d, called %q", code, f.called)
	}
}

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{fmt.Errorf("lock: %w", step.ErrBusy), ExitBusy},
		{&launch.CheckError{Check: launch.CheckRules, Err: errors.New("x")}, ExitRefused},
		{&launch.BootError{Err: errors.New("x")}, ExitBoot},
		{launch.ErrTimeout, ExitTimeout},
		{errors.Join(launch.ErrTimeout, launch.ErrStuck), ExitTimeout},
		{&launch.ExtractError{Err: launch.ErrTooLarge}, ExitResults},
		{context.Canceled, ExitInterrupted},
		{errors.New("something else"), ExitFailure},
	} {
		f := &fakeOps{err: tc.err}
		var stderr bytes.Buffer
		if code := run([]string{id}, &stderr, 0, f); code != tc.want {
			t.Errorf("%v: exit %d, want %d", tc.err, code, tc.want)
		}
		if tc.err != nil && !strings.Contains(stderr.String(), "cc-launch: ") {
			t.Errorf("%v: stderr %q doesn't explain", tc.err, stderr.String())
		}
	}
}
