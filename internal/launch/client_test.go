package launch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// fakeStreamer records the root command and writes a scripted console.
type fakeStreamer struct {
	got     system.Cmd
	console string
	code    int
	err     error
}

func (f *fakeStreamer) RunStreaming(_ context.Context, c system.Cmd, console io.Writer) (system.Result, error) {
	f.got = c
	console.Write([]byte(f.console))
	return system.Result{Code: f.code}, f.err
}

func TestClientArgumentForms(t *testing.T) {
	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	for name, tc := range map[string]struct {
		call func(Client) error
		want []string
	}{
		"run":   {func(c Client) error { return c.Run(context.Background(), id) }, []string{id.String()}},
		"build": {func(c Client) error { return c.Build(context.Background(), id) }, []string{"build", id.String()}},
		"clean": {func(c Client) error { return c.Clean(context.Background(), id) }, []string{"clean", id.String()}},
	} {
		f := &fakeStreamer{console: "serial line\n"}
		var console bytes.Buffer
		c := Client{Priv: f, Console: &console}
		if err := tc.call(c); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if f.got.Name != "/usr/local/lib/codecheck-sandbox/bin/cc-launch" || !slices.Equal(f.got.Args, tc.want) {
			t.Errorf("%s ran %s %v", name, f.got.Name, f.got.Args)
		}
		if console.String() != "serial line\n" {
			t.Errorf("%s: console = %q", name, console.String())
		}
	}
}

func TestClientExplainsExitCodes(t *testing.T) {
	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	for code, want := range map[int]string{
		ExitBusy: "another sandbox run", ExitRefused: "check", ExitTimeout: "time limit",
		ExitEnvironment: "setup", ExitBoot: "start", ExitResults: "results",
	} {
		f := &fakeStreamer{code: code}
		err := Client{Priv: f, Console: io.Discard}.Run(context.Background(), id)
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Code != code || !strings.Contains(err.Error(), want) {
			t.Errorf("exit %d: err = %v, want an ExitError mentioning %q", code, err, want)
		}
	}
}

func TestClientPassesLoginErrorsOn(t *testing.T) {
	id, _ := ParseRunID("cc-20261121-101500-a1b2c3")
	f := &fakeStreamer{code: 1, err: system.ErrLoginExpired}
	if err := (Client{Priv: f, Console: io.Discard}).Run(context.Background(), id); !errors.Is(err, system.ErrLoginExpired) {
		t.Errorf("err = %v, want ErrLoginExpired", err)
	}
}
