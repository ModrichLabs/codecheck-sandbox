package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

const plantedKey = "sk-ant-api03-PLANTEDsecretKEYvalue_0123456789-abcdefghij"

// bundleEnv gives doctor a healthy fake host and a log holding secrets.
func bundleEnv(t *testing.T) Env {
	env := doctorEnv(checkStep{id: "platform-kvm", title: "KVM access", status: step.Status{OK: true, Detail: "/dev/kvm usable"}})
	logs := env.LogDir()
	env.FS.MkdirAll(logs, 0o700)
	env.FS.WriteFile(filepath.Join(logs, "codecheck-sandbox.log"),
		[]byte(`{"msg":"proxy start","key":"`+plantedKey+`","path":"/home/u/secret-project/main.go"}`+"\n"), 0o600)
	env.FS.WriteFile(filepath.Join(logs, "codecheck-sandbox.log.1"), []byte(`{"msg":"older","auth":"Bearer `+plantedKey+`"}`+"\n"), 0o600)
	env.FS.WriteFile(filepath.Join(logs, "codecheck-sandbox.log.4"), []byte(`{"msg":"oldest"}`+"\n"), 0o600)
	return env
}

func readBundle(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func TestBundleNeverHoldsTheKey(t *testing.T) {
	env := bundleEnv(t)
	dest := filepath.Join(t.TempDir(), "bundle.tar.gz")
	out, err := runIn(t, env, "doctor", "--bundle", "--out", dest)
	if err != nil {
		t.Fatalf("doctor --bundle: %v\n%s", err, out)
	}
	files := readBundle(t, dest)
	var names []string
	for name, body := range files {
		names = append(names, name)
		if strings.Contains(body, plantedKey) || strings.Contains(body, "PLANTEDsecret") {
			t.Errorf("%s holds the planted key", name)
		}
		if strings.Contains(body, "/home/u/") {
			t.Errorf("%s holds the home folder's path", name)
		}
	}
	slices.Sort(names)
	want := []string{"doctor.txt", "environment.txt", "logs/codecheck-sandbox.log", "logs/codecheck-sandbox.log.1", "versions.txt"}
	if !slices.Equal(names, want) {
		t.Errorf("bundle holds %v, want %v", names, want)
	}
	if !strings.Contains(files["doctor.txt"], "KVM access") || !strings.Contains(files["versions.txt"], "codecheck-sandbox") {
		t.Errorf("doctor.txt %q, versions.txt %q", files["doctor.txt"], files["versions.txt"])
	}
	// The file list prints before the archive gets written.
	list, written, _ := strings.Cut(out, dest)
	if !strings.Contains(list, "logs/codecheck-sandbox.log.1") || written == "" {
		t.Errorf("output:\n%s", out)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode %v, want 0600", fi.Mode().Perm())
	}
}

// A failing doctor still writes the bundle; people need one most then.
func TestBundleOnAFailingHost(t *testing.T) {
	env := doctorEnv(checkStep{id: "host-rules", title: "Firewall rules", status: step.Status{Detail: "differ"}})
	dest := filepath.Join(t.TempDir(), "b.tar.gz")
	if _, err := runIn(t, env, "doctor", "--bundle", "--out", dest); err != nil {
		t.Errorf("doctor --bundle failed with the host: %v", err)
	}
	if !strings.Contains(readBundle(t, dest)["doctor.txt"], "differ") {
		t.Error("doctor.txt lacks the failure")
	}
}

func TestBundleRefusesToOverwrite(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "b.tar.gz")
	os.WriteFile(dest, []byte("keep me"), 0o600)
	if _, err := runIn(t, bundleEnv(t), "doctor", "--bundle", "--out", dest); err == nil {
		t.Error("the bundle overwrote a file")
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, []byte("keep me")) {
		t.Error("the existing file changed")
	}
}

func TestRedactBundleText(t *testing.T) {
	in := "key " + plantedKey + " and sk-ant-admin01-ABCDEFGHIJKLMNOP in /home/u/x and /home/user2/y"
	got := redactBundle(in, "/home/u")
	if strings.Contains(got, "sk-ant-api03-P") || strings.Contains(got, "ABCDEFGHIJ") || strings.Contains(got, "/home/u/") {
		t.Errorf("redactBundle = %q", got)
	}
	if !strings.Contains(got, "~/x") || !strings.Contains(got, "/home/user2/y") {
		t.Errorf("redactBundle lost too much: %q", got)
	}
}
