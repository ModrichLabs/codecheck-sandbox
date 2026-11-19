package system

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// FakeRunner stands in for a real Runner in tests. Script each expected call
// with On; every Run consumes the first unused script whose argv prefix
// matches. A call with no matching script panics, so a test never runs a
// command it did not plan for.
type FakeRunner struct {
	mu      sync.Mutex
	scripts []*FakeCall
	calls   []Cmd
}

// FakeCall holds one scripted response.
type FakeCall struct {
	prefix []string
	res    Result
	err    error
	used   bool
}

// On scripts a response for the next call whose argv (Name, then Args) starts
// with prefix. Finish it with Return or Fail.
func (f *FakeRunner) On(prefix ...string) *FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &FakeCall{prefix: prefix}
	f.scripts = append(f.scripts, c)
	return c
}

// Return makes the scripted call produce res.
func (c *FakeCall) Return(res Result) { c.res = res }

// Fail makes the scripted call return err.
func (c *FakeCall) Fail(err error) { c.err = err }

// Run implements Runner.
func (f *FakeRunner) Run(_ context.Context, c Cmd) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	argv := append([]string{c.Name}, c.Args...)
	for _, s := range f.scripts {
		if !s.used && len(argv) >= len(s.prefix) && slices.Equal(argv[:len(s.prefix)], s.prefix) {
			s.used = true
			return s.res, s.err
		}
	}
	panic(fmt.Sprintf("FakeRunner: unexpected call: %s", strings.Join(argv, " ")))
}

// Calls returns every command Run received, in order.
func (f *FakeRunner) Calls() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Pending lists the argv prefixes of scripts no call has used yet.
func (f *FakeRunner) Pending() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.scripts {
		if !s.used {
			out = append(out, strings.Join(s.prefix, " "))
		}
	}
	return out
}
