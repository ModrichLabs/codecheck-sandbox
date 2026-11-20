package hostnet

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ModrichLabs/codecheck-sandbox/internal/proxy"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

const (
	idProxyBinary  = "host-proxy-binary"
	idProxyCA      = "host-proxy-ca"
	idAPIKey       = "host-api-key"
	idProxyService = "host-proxy-service"
)

// --- Proxy binary ---

// proxyBinary installs a root-owned copy of the running CLI, which the proxy
// unit runs; a user-owned binary would let the user rewrite the proxy.
func (d Deps) proxyBinary() step.Step {
	dest := d.Config.ProxyBinary
	self := func() (string, error) {
		if d.Executable != nil {
			return d.Executable()
		}
		return os.Executable()
	}
	install := func(src string) system.Cmd {
		return root("install", "-D", "-m", "0755", "-o", "root", "-g", "root", "-T", src, dest)
	}
	return hostStep{
		id: idProxyBinary, title: "Proxy binary " + dest,
		check: func(context.Context) (step.Status, error) {
			src, err := self()
			if err != nil {
				return step.Status{}, err
			}
			want, err := d.FS.ReadFile(src)
			if err != nil {
				return step.Status{}, fmt.Errorf("reading the running binary: %w", err)
			}
			got, err := d.FS.ReadFile(dest)
			if err != nil {
				return fail("no " + dest)
			}
			if sha256.Sum256(got) != sha256.Sum256(want) {
				return fail(dest + " differs from this release")
			}
			return pass(dest + " matches this release")
		},
		plan: func(context.Context) (step.Plan, error) {
			src, err := self()
			if err != nil {
				return step.Plan{}, err
			}
			return step.Plan{Cmds: []step.PlannedCmd{install(src)}, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			src, err := self()
			if err != nil {
				return err
			}
			// Note the folders install -D will create, top down.
			var missing []string
			for dir := path.Dir(dest); !d.exists(dir); dir = path.Dir(dir) {
				missing = append([]string{dir}, missing...)
			}
			existed := d.exists(dest)
			if err := d.run(ctx, install(src)); err != nil {
				return err
			}
			for _, dir := range missing {
				d.record(step.Item{ID: idProxyBinary, Kind: step.KindDir, Path: dir, State: step.Created})
			}
			if !existed {
				d.record(step.Item{ID: idProxyBinary, Kind: step.KindFile, Path: dest, State: step.Created})
			}
			return nil
		},
		undo: d.undoFiles,
	}
}

// --- Sandbox CA ---

const caKeyShown = "(the CA's private key: generated now, written root-only, never shown)\n"

// proxyCA generates the sandbox CA once, so the plan shows the very
// certificate Apply writes. A new CA means a guest rebuild, since the guest
// image trusts the certificate.
func (d Deps) proxyCA() step.Step {
	c := d.Config
	var (
		once            sync.Once
		certPEM, keyPEM []byte
		genErr          error
	)
	generate := func() error {
		once.Do(func() { certPEM, keyPEM, genErr = proxy.NewCA(time.Now()) })
		return genErr
	}
	dir := path.Dir(c.CACertPath)
	return hostStep{
		id: idProxyCA, title: "Sandbox CA",
		check: func(context.Context) (step.Status, error) {
			b, err := d.FS.ReadFile(c.CACertPath)
			if err != nil {
				return fail("no " + c.CACertPath)
			}
			if !d.exists(c.CAKeyPath) {
				return fail("no " + c.CAKeyPath)
			}
			if err := checkCACert(b, time.Now()); err != nil {
				return fail(err.Error())
			}
			return pass("CA ready, limited to " + proxy.APIHost)
		},
		plan: func(context.Context) (step.Plan, error) {
			if err := generate(); err != nil {
				return step.Plan{}, err
			}
			return step.Plan{
				Files: []step.PlannedFile{
					{Path: c.CAKeyPath, Mode: 0o600, Content: []byte(caKeyShown)},
					{Path: c.CACertPath, Mode: 0o644, Content: certPEM},
				},
				Cmds:      []step.PlannedCmd{root("install", "-d", "-m", "0755", dir)},
				NeedsRoot: true,
			}, nil
		},
		apply: func(ctx context.Context) error {
			if err := generate(); err != nil {
				return err
			}
			if err := d.ensureDir(ctx, idProxyCA, dir); err != nil {
				return err
			}
			// The key first, so a certificate never sits without its key.
			if err := system.WriteRootFile(ctx, d.Runner, c.CAKeyPath, keyPEM, 0o600); err != nil {
				return err
			}
			d.record(step.Item{ID: idProxyCA, Kind: step.KindFile, Path: c.CAKeyPath, State: step.Created})
			if err := system.WriteRootFile(ctx, d.Runner, c.CACertPath, certPEM, 0o644); err != nil {
				return err
			}
			d.record(step.Item{ID: idProxyCA, Kind: step.KindFile, Path: c.CACertPath, State: step.Created})
			return nil
		},
		undo: d.undoFiles,
	}
}

// checkCACert accepts a CA certificate limited to the API host that stays
// valid for at least another year.
func checkCACert(certPEM []byte, now time.Time) error {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return errors.New("the CA certificate holds no PEM certificate")
	}
	cert, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return fmt.Errorf("the CA certificate: %w", err)
	}
	if !cert.IsCA || len(cert.PermittedDNSDomains) != 1 || cert.PermittedDNSDomains[0] != proxy.APIHost {
		return errors.New("the CA certificate lacks its name constraint")
	}
	if cert.NotAfter.Before(now.AddDate(1, 0, 0)) {
		return fmt.Errorf("the CA expires on %s; renew it", cert.NotAfter.Format("2006-01-02"))
	}
	return nil
}

// --- API key ---

const apiKeyShown = "(your Anthropic API key, hidden)\n"

// apiKey writes the key the wizard collected, root-only; only the proxy
// service reads it, through LoadCredential=.
func (d Deps) apiKey() step.Step {
	p := d.Config.APIKeyPath
	return hostStep{
		id: idAPIKey, title: "API key for the proxy",
		check: func(context.Context) (step.Status, error) {
			info, err := d.FS.Stat(p)
			if err != nil {
				return fail("no API key stored")
			}
			if info.Mode().Perm() != 0o600 {
				return fail(fmt.Sprintf("%s has mode %04o, want 0600", p, info.Mode().Perm()))
			}
			return pass("API key stored, root-only")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{
				Files:     []step.PlannedFile{{Path: p, Mode: 0o600, Content: []byte(apiKeyShown)}},
				NeedsRoot: true,
			}, nil
		},
		apply: func(ctx context.Context) error {
			if d.APIKey == nil {
				return errors.New("no API key supplied")
			}
			key, err := d.APIKey()
			if err != nil {
				return err
			}
			if err := checkAPIKey(key); err != nil {
				return err
			}
			if err := system.WriteRootFile(ctx, d.Runner, p, []byte(key+"\n"), 0o600); err != nil {
				return err
			}
			d.record(step.Item{ID: idAPIKey, Kind: step.KindFile, Path: p, State: step.Created})
			return nil
		},
		undo: d.undoFiles,
	}
}

// checkAPIKey refuses anything shaped unlike an Anthropic API key, without repeating
// the value.
func checkAPIKey(key string) error {
	switch {
	case key == proxy.Placeholder:
		return errors.New("the API key holds the guest placeholder")
	case !strings.HasPrefix(key, "sk-ant-") || len(key) > 512:
		return errors.New("the API key does not look like an Anthropic key (sk-ant-…)")
	case strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return errors.New("the API key holds spaces or control characters")
	}
	return nil
}

// --- Proxy service ---

func (d Deps) proxyService() step.Step {
	unitFile := d.rendered(d.Config.ProxyUnitPath)
	unit := path.Base(d.Config.ProxyUnitPath)
	cmds := []system.Cmd{
		root("systemctl", "daemon-reload"),
		root("systemctl", "enable", unit),
		root("systemctl", "restart", unit),
	}
	return hostStep{
		id: idProxyService, title: "Egress proxy service",
		check: func(ctx context.Context) (step.Status, error) {
			if !d.matches(unitFile.Path, unitFile.Content) {
				return fail(unitFile.Path + " missing or changed")
			}
			c, err := d.code(ctx, system.Cmd{Name: "systemctl", Args: []string{"is-enabled", "--quiet", unit}})
			if err != nil {
				return step.Status{}, err
			}
			if c != 0 {
				return fail(unit + " not enabled")
			}
			active, err := d.serviceActive(ctx, unit)
			if err != nil {
				return step.Status{}, err
			}
			if !active {
				return fail(unit + " not running")
			}
			return pass(unit + " running")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{Files: []step.PlannedFile{unitFile}, Cmds: cmds, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			if err := d.writeFile(ctx, unitFile); err != nil {
				return err
			}
			d.record(step.Item{ID: idProxyService, Kind: step.KindFile, Path: unitFile.Path, State: step.Created})
			if err := d.runAll(ctx, cmds); err != nil {
				return err
			}
			d.record(step.Item{ID: idProxyService, Kind: step.KindUnit, Name: unit, State: step.Created})
			return nil
		},
		undo: func(ctx context.Context, items []step.Item) error {
			var errs []error
			for _, it := range items {
				if it.Kind == step.KindUnit {
					errs = append(errs, d.run(ctx, root("systemctl", "disable", "--now", it.Name)))
				}
			}
			errs = append(errs, d.undoFiles(ctx, items), d.run(ctx, root("systemctl", "daemon-reload")))
			return errors.Join(errs...)
		},
	}
}
