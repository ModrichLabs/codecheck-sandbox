package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/ModrichLabs/codecheck-sandbox/internal/buildinfo"
	"github.com/ModrichLabs/codecheck-sandbox/internal/logging"
	"github.com/ModrichLabs/codecheck-sandbox/internal/pins"
	"github.com/ModrichLabs/codecheck-sandbox/internal/theme"
	"github.com/ModrichLabs/codecheck-sandbox/internal/tui"
)

// addBundleFlags gives doctor --bundle and --out.
func addBundleFlags(cmd *cobra.Command, env Env, opts *Options) {
	var bundle bool
	var out string
	cmd.Flags().BoolVar(&bundle, "bundle", false, "also write a redacted support bundle (logs, doctor output, versions) to share by hand")
	cmd.Flags().StringVar(&out, "out", "", "where --bundle writes (default: ./codecheck-sandbox-bundle-<time>.tar.gz)")
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !bundle {
			if out != "" {
				return errors.New("--out names the bundle's file; pass --bundle with it")
			}
			return run(cmd, args)
		}
		return writeBundle(cmd, env, opts, out)
	}
}

type bundleFile struct {
	name string
	body []byte
}

// writeBundle runs doctor, then writes the support bundle: the file list
// first, then the archive. It succeeds whatever doctor found, since a
// failing host makes the bundle most useful.
func writeBundle(cmd *cobra.Command, env Env, opts *Options, dest string) error {
	out := cmd.OutOrStdout()
	if dest == "" {
		dest = "codecheck-sandbox-bundle-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	}
	rows, err := doctorRows(cmd.Context(), env, opts)
	if err != nil {
		return err
	}
	files := []bundleFile{{name: "doctor.txt", body: []byte(ansi.Strip(tui.DoctorView(theme.New(true), rows, 100)) + "\n")}}
	for _, name := range []string{logging.FileName, logging.FileName + ".1"} {
		b, err := env.FS.ReadFile(filepath.Join(env.LogDir(), name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		files = append(files, bundleFile{name: "logs/" + name, body: b})
	}
	files = append(files, bundleFile{name: "versions.txt", body: []byte(versionsText(env))},
		bundleFile{name: "environment.txt", body: []byte(environmentText(env))})

	for i := range files {
		files[i].body = []byte(redactBundle(string(files[i].body), env.Home))
	}
	fmt.Fprintln(out, "The support bundle holds, with API keys and your home folder's path removed:")
	for _, f := range files {
		fmt.Fprintf(out, "  %s (%d bytes)\n", f.name, len(f.body))
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range files {
		body := f.body
		h := &tar.Header{Name: f.name, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now().UTC(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("writing the bundle: %w", err)
	}
	_, err = f.Write(buf.Bytes())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return err
	}
	fmt.Fprintf(out, "Wrote %s. Read it before you share it.\n", dest)
	return nil
}

// keyPattern matches Anthropic API keys of any kind.
var keyPattern = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{8,}`)

// redactBundle removes API keys and the home folder's path from text the
// bundle carries. Logs pass through the Redactor when written; this catches
// anything shaped like a key that got there another way.
func redactBundle(s, home string) string {
	s = keyPattern.ReplaceAllString(s, "sk-ant-[redacted]")
	if home = filepath.Clean(home); filepath.IsAbs(home) && home != "/" {
		re := regexp.MustCompile(regexp.QuoteMeta(home) + `(/|$|[^A-Za-z0-9_.-])`)
		s = re.ReplaceAllString(s, "~$1")
	}
	return s
}

func versionsText(env Env) string {
	commit := pins.CodeCheck.Commit
	if c, _, err := env.LoadConfig(); err == nil {
		commit = c.CodeCheckCommit
	}
	return fmt.Sprintf("%s %s\ngo %s\nfirecracker %s\nkernel %s\nclaude-code %s\ncodecheck %s\n",
		buildinfo.Name, buildinfo.Version, runtime.Version(), pins.Firecracker.Version, pins.Kernel.Version,
		pins.ClaudeCode.Version, commit)
}

func environmentText(env Env) string {
	var b strings.Builder
	fmt.Fprintf(&b, "arch %s\ncpus %d\n", runtime.GOARCH, runtime.NumCPU())
	var u unix.Utsname
	if unix.Uname(&u) == nil {
		fmt.Fprintf(&b, "kernel %s\n", unix.ByteSliceToString(u.Release[:]))
	}
	if osr, err := env.FS.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(osr), "\n") {
			if k, _, _ := strings.Cut(line, "="); k == "ID" || k == "ID_LIKE" || k == "VERSION_ID" || k == "PRETTY_NAME" {
				b.WriteString(line + "\n")
			}
		}
	}
	return b.String()
}
