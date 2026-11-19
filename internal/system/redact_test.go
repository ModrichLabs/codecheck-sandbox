package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

const testKey = "sk-ant-api03-SECRETSECRET"

func TestRedactReplacesEveryOccurrence(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	r.Add("short")
	r.Add("") // ignored: an empty secret must not match everywhere
	got := r.RedactString("a " + testKey + " b " + testKey + " short")
	if want := "a [redacted] b [redacted] [redacted]"; got != want {
		t.Errorf("RedactString = %q, want %q", got, want)
	}
}

func TestRedactPrefersTheLongestSecret(t *testing.T) {
	r := &Redactor{}
	r.Add("abc")
	r.Add("abcdef")
	if got := r.RedactString("xabcdefx"); got != "x[redacted]x" {
		t.Errorf("RedactString = %q, want x[redacted]x", got)
	}
}

func TestRedactMergesOverlappingSecrets(t *testing.T) {
	r := &Redactor{}
	r.Add("abcXYZ")
	r.Add("XYZdef")
	if got := r.RedactString("1abcXYZdef2"); got != "1[redacted]2" {
		t.Errorf("RedactString = %q, want 1[redacted]2", got)
	}
}

// failOnce fails its first write, then records the rest.
type failOnce struct {
	failed bool
	buf    bytes.Buffer
}

func (f *failOnce) Write(p []byte) (int, error) {
	if !f.failed {
		f.failed = true
		return 0, errors.New("disk full")
	}
	return f.buf.Write(p)
}

func TestRedactingWriterRetryAfterFailure(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	dst := &failOnce{}
	w := r.Writer(dst)
	// The first attempt fails while the writer holds back the key's start;
	// the caller retries the same bytes, as a logger would.
	p := []byte("x " + testKey[:5])
	if _, err := w.Write(p); err == nil {
		t.Fatal("first write: want the disk-full error")
	}
	if _, err := w.Write(p); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if _, err := w.Write([]byte(testKey[5:] + "\n")); err != nil {
		t.Fatalf("third write: %v", err)
	}
	_ = w.Close()
	if got, want := dst.buf.String(), "x [redacted]\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// writeChunks writes text through a fresh redacting writer in chunks of size n.
func writeChunks(t *testing.T, r *Redactor, text string, n int) string {
	t.Helper()
	var buf bytes.Buffer
	w := r.Writer(&buf)
	for i := 0; i < len(text); i += n {
		end := min(i+n, len(text))
		if k, err := w.Write([]byte(text[i:end])); err != nil || k != end-i {
			t.Fatalf("Write = %d, %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.String()
}

func TestRedactingWriterCatchesSecretsSplitAcrossWrites(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	text := "key=" + testKey + " then sk-ant-not-it and " + testKey
	for n := 1; n <= len(text); n++ {
		got := writeChunks(t, r, text, n)
		if strings.Contains(got, testKey) {
			t.Fatalf("chunk size %d leaked the key: %q", n, got)
		}
		if want := "key=[redacted] then sk-ant-not-it and [redacted]"; got != want {
			t.Fatalf("chunk size %d: got %q, want %q", n, got, want)
		}
	}
}

func TestRedactingWriterPassesCompleteLinesAtOnce(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	var buf bytes.Buffer
	w := r.Writer(&buf)
	fmt.Fprint(w, "plain line\n")
	if buf.String() != "plain line\n" {
		t.Errorf("before Close: %q, want the whole line", buf.String())
	}
}

func TestRedactingWriterUsesSecretsAddedLater(t *testing.T) {
	r := &Redactor{}
	var buf bytes.Buffer
	w := r.Writer(&buf)
	r.Add(testKey)
	fmt.Fprint(w, testKey+"\n")
	_ = w.Close()
	if strings.Contains(buf.String(), testKey) {
		t.Errorf("leaked: %q", buf.String())
	}
}

func TestKeyNeverReachesTheLog(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	var buf bytes.Buffer
	w := r.Writer(&buf)
	log := slog.New(slog.NewJSONHandler(w, nil))
	log.Info("saved settings", "api_key", testKey, "note", "prefix "+testKey[:10])
	log.Error("request failed", "err", errors.New("401 for key "+testKey))
	_ = w.Close()
	if strings.Contains(buf.String(), testKey) {
		t.Errorf("log leaked the key:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "prefix "+testKey[:10]) {
		t.Errorf("log lost a harmless prefix of the key:\n%s", buf.String())
	}
}

func TestKeyNeverReachesRunnerErrors(t *testing.T) {
	r := &Redactor{}
	r.Add(testKey)
	boom := errors.New("curl: bad header x-api-key: " + testKey)
	f := &FakeRunner{}
	f.On("curl").Fail(boom)
	f.On("claude").Return(Result{Code: 1, Stdout: []byte(testKey), Stderr: []byte("invalid key " + testKey)})
	run := r.Runner(f)

	_, err := run.Run(context.Background(), Cmd{Name: "curl"})
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("error = %v, want one without the key", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("redacted error lost its chain: %v", err)
	}

	res, err := run.Run(context.Background(), Cmd{Name: "claude"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(string(res.Stderr), testKey) {
		t.Errorf("stderr leaked the key: %q", res.Stderr)
	}
	if string(res.Stdout) != testKey {
		t.Errorf("stdout = %q; Runner must leave stdout alone", res.Stdout)
	}
	if err := runChecked(context.Background(), r.Runner(scripted("claude", Result{Code: 1, Stderr: []byte(testKey)})), Cmd{Name: "claude"}); err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("runChecked error = %v, want one without the key", err)
	}
}

func scripted(name string, res Result) *FakeRunner {
	f := &FakeRunner{}
	f.On(name).Return(res)
	return f
}

func TestRedactorConcurrentUse(t *testing.T) {
	r := &Redactor{}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			r.Add(fmt.Sprintf("secret-%d", i))
			_ = r.RedactString("secret-0 secret-7")
		})
	}
	wg.Wait()
	if got := r.RedactString("secret-0 secret-7"); got != "[redacted] [redacted]" {
		t.Errorf("RedactString = %q", got)
	}
}
