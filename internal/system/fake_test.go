package system

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFakeRunnerReplaysByPrefix(t *testing.T) {
	f := &FakeRunner{}
	f.On("ip", "-j", "link").Return(Result{Stdout: []byte("[]")})

	res, err := f.Run(context.Background(), Cmd{Name: "ip", Args: []string{"-j", "link", "show", "tap0"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "[]" {
		t.Errorf("stdout = %q, want []", res.Stdout)
	}
}

func TestFakeRunnerAnswersInOrderAndOnce(t *testing.T) {
	f := &FakeRunner{}
	f.On("sudo", "-n", "-v").Return(Result{Code: 0})
	f.On("sudo", "-n", "-v").Return(Result{Code: 1})

	c := Cmd{Name: "sudo", Args: []string{"-n", "-v"}}
	first, _ := f.Run(context.Background(), c)
	second, _ := f.Run(context.Background(), c)
	if first.Code != 0 || second.Code != 1 {
		t.Errorf("codes = %d, %d; want 0, 1", first.Code, second.Code)
	}
	if p := f.Pending(); len(p) != 0 {
		t.Errorf("Pending() = %v, want none", p)
	}
}

func TestFakeRunnerFail(t *testing.T) {
	boom := errors.New("boom")
	f := &FakeRunner{}
	f.On("nft").Fail(boom)
	if _, err := f.Run(context.Background(), Cmd{Name: "nft"}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	f := &FakeRunner{}
	f.On("tee").Return(Result{})
	c := Cmd{Name: "tee", Args: []string{"/etc/x"}, Stdin: []byte("data"), Root: true}
	_, _ = f.Run(context.Background(), c)
	if want := []Cmd{c}; !reflect.DeepEqual(f.Calls(), want) {
		t.Errorf("Calls() = %#v, want %#v", f.Calls(), want)
	}
}

func TestFakeRunnerPanicsOnUnexpectedCall(t *testing.T) {
	f := &FakeRunner{}
	f.On("ip", "link").Return(Result{})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic for an unscripted call")
		}
		if msg := r.(string); !strings.Contains(msg, "rm -rf /") {
			t.Errorf("panic message %q should name the argv", msg)
		}
	}()
	_, _ = f.Run(context.Background(), Cmd{Name: "rm", Args: []string{"-rf", "/"}})
}

func TestFakeRunnerPendingListsUnusedScripts(t *testing.T) {
	f := &FakeRunner{}
	f.On("visudo", "-cf").Return(Result{})
	if got, want := f.Pending(), []string{"visudo -cf"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Pending() = %v, want %v", got, want)
	}
}
