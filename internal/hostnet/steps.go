package hostnet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ModrichLabs/codecheck-sandbox/internal/rulehash"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
	"github.com/ModrichLabs/codecheck-sandbox/internal/telemetry"
)

// Deps supplies what the host steps need.
type Deps struct {
	Config Config
	// Runner runs Root commands through the privilege tool (a PrivRunner).
	Runner system.Runner
	// FS reads files a normal user may read, such as the rendered ones.
	FS system.FS
	// Record hears every item Apply creates, for the manifest; nil drops them.
	Record func(step.Item)
	// APIKey supplies the key the wizard collected; nil means none.
	APIKey func() (string, error)
	// Executable names the running binary, which the proxy step installs;
	// nil means os.Executable.
	Executable func() (string, error)
	// User names the account the sudo rule lets run cc-launch: the person
	// running setup.
	User string

	// proxyChanged notes that a step replaced something the running proxy
	// loaded at start (its binary, CA or key), so the service must restart.
	// Steps creates it, so all the steps of one run share it.
	proxyChanged *atomic.Bool
}

// selinuxEnforcing reports whether SELinux enforces on this host.
func (d Deps) selinuxEnforcing() bool {
	b, err := d.FS.ReadFile("/sys/fs/selinux/enforce")
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// relabel gives new files their default SELinux labels straight away, so a
// service started later in the same run can use them; without enforcing
// SELinux it does nothing.
func (d Deps) relabel(ctx context.Context, paths ...string) error {
	if !d.selinuxEnforcing() {
		return nil
	}
	return d.run(ctx, root("restorecon", append([]string{"-F", "--"}, paths...)...))
}

const (
	idJailUser   = "host-jail-user"
	idJailerBase = "host-jailer-base"
	idRules      = "host-rules"
	idNM         = "host-nm-unmanaged"
	idTap        = "host-tap-service"
	idFirewalld  = "host-firewalld"
	idUfw        = "host-ufw"
	idSELinux    = "host-selinux"
)

func init() {
	telemetry.RegisterStepIDs(idJailUser, idJailerBase, idRules, idNM, idTap, idFirewalld, idUfw, idSELinux,
		idProxyBinary, idProxyCA, idAPIKey, idProxyService, idLauncher, idLock, idSudoers)
}

// Steps returns the host phase in order: the rules load before the tap
// service starts (which loads them again), and NetworkManager learns to
// ignore the tap before the tap exists.
// It refuses an invalid Config, which could otherwise render empty files.
func Steps(d Deps) ([]step.Step, error) {
	if err := d.Config.Validate(); err != nil {
		return nil, err
	}
	d.proxyChanged = &atomic.Bool{}
	// The engine runs Apply again after a login handover, so the same item
	// can come twice; the manifest keeps each once.
	if rec := d.Record; rec != nil {
		seen := map[step.Item]bool{}
		d.Record = func(it step.Item) {
			if !seen[it] {
				seen[it] = true
				rec(it)
			}
		}
	}
	return []step.Step{
		d.jailUser(), d.jailerBase(), d.rules(), d.nmUnmanaged(),
		d.tapService(), d.firewalld(), d.ufw(),
		d.proxyBinary(), d.proxyCA(), d.apiKey(), d.proxyService(),
		d.launcher(), d.lock(), d.sudoers(),
		d.selinux(), // last, so it labels everything above
	}, nil
}

// hostStep adapts plain functions to step.Step.
type hostStep struct {
	id, title string
	check     func(context.Context) (step.Status, error)
	plan      func(context.Context) (step.Plan, error)
	apply     func(context.Context) error
	undo      func(context.Context, []step.Item) error
}

func (s hostStep) ID() string                                        { return s.id }
func (s hostStep) Title() string                                     { return s.title }
func (s hostStep) Phase() step.Phase                                 { return step.PhaseHost }
func (s hostStep) Check(ctx context.Context) (step.Status, error)    { return s.check(ctx) }
func (s hostStep) Plan(ctx context.Context) (step.Plan, error)       { return s.plan(ctx) }
func (s hostStep) Apply(ctx context.Context) error                   { return s.apply(ctx) }
func (s hostStep) Undo(ctx context.Context, items []step.Item) error { return s.undo(ctx, items) }

func pass(detail string) (step.Status, error) { return step.Status{OK: true, Detail: detail}, nil }
func fail(detail string) (step.Status, error) { return step.Status{Detail: detail}, nil }

func root(name string, args ...string) system.Cmd {
	return system.Cmd{Name: name, Args: args, Root: true}
}

func (d Deps) record(it step.Item) {
	if d.Record != nil {
		d.Record(it)
	}
}

// code runs c and returns its exit code.
func (d Deps) code(ctx context.Context, c system.Cmd) (int, error) {
	res, err := d.Runner.Run(ctx, c)
	return res.Code, err
}

// run runs c and turns a non-zero exit into an error carrying stderr.
func (d Deps) run(ctx context.Context, c system.Cmd) error {
	res, err := d.Runner.Run(ctx, c)
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s %s: exit %d: %s", c.Name, strings.Join(c.Args, " "), res.Code, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func (d Deps) runAll(ctx context.Context, cmds []system.Cmd) error {
	for _, c := range cmds {
		if err := d.run(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (d Deps) exists(p string) bool { _, err := d.FS.Stat(p); return err == nil }

// matches reports whether p holds exactly want.
func (d Deps) matches(p string, want []byte) bool {
	got, err := d.FS.ReadFile(p)
	return err == nil && bytes.Equal(got, want)
}

func (d Deps) rendered(p string) step.PlannedFile {
	files, _ := Render(d.Config) // Steps validated the Config already
	for _, f := range files {
		if f.Path == p {
			return f
		}
	}
	return step.PlannedFile{Path: p}
}

func (d Deps) writeFile(ctx context.Context, f step.PlannedFile) error {
	return system.WriteRootFile(ctx, d.Runner, f.Path, f.Content, f.Mode)
}

// ensureDir creates a root-owned folder, recording it only when it did not
// exist before, so uninstall never removes a folder setup found.
func (d Deps) ensureDir(ctx context.Context, id, p string) error {
	existed := d.exists(p)
	if err := d.run(ctx, root("install", "-d", "-m", "0755", p)); err != nil {
		return err
	}
	if !existed {
		d.record(step.Item{ID: id, Kind: step.KindDir, Path: p, State: step.Created})
	}
	return nil
}

// undoFiles removes file items, then empty folder items, deepest first so a
// parent goes after its children.
func (d Deps) undoFiles(ctx context.Context, items []step.Item) error {
	var errs []error
	var dirs []string
	for _, it := range items {
		switch it.Kind {
		case step.KindFile:
			errs = append(errs, d.run(ctx, root("rm", "-f", "--", it.Path)))
		case step.KindDir:
			dirs = append(dirs, it.Path)
		}
	}
	slices.SortStableFunc(dirs, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, p := range dirs {
		errs = append(errs, d.run(ctx, root("rmdir", "--ignore-fail-on-non-empty", "--", p)))
	}
	return errors.Join(errs...)
}

func hasNetwork(items []step.Item, name string) bool {
	for _, it := range items {
		if it.Kind == step.KindNetwork && it.Name == name {
			return true
		}
	}
	return false
}

// --- Jail user ---

func (d Deps) jailUser() step.Step {
	u := d.Config.JailUser
	add := root("useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/bin/false", u)
	return hostStep{
		id: idJailUser, title: "Jail user " + u,
		check: func(ctx context.Context) (step.Status, error) {
			c, err := d.code(ctx, system.Cmd{Name: "getent", Args: []string{"passwd", u}})
			if err != nil {
				return step.Status{}, err
			}
			if c != 0 {
				return fail("no user " + u)
			}
			return pass(u + " exists")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{Cmds: []step.PlannedCmd{add}, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			if err := d.run(ctx, add); err != nil {
				return err
			}
			d.record(step.Item{ID: idJailUser, Kind: step.KindUser, Name: u, State: step.Created})
			return nil
		},
		undo: func(ctx context.Context, items []step.Item) error {
			var errs []error
			for _, it := range items {
				if it.Kind == step.KindUser {
					errs = append(errs, d.run(ctx, root("userdel", it.Name)))
				}
			}
			return errors.Join(errs...)
		},
	}
}

// --- Jailer base ---

func (d Deps) jailerBase() step.Step {
	p := d.Config.JailerBase
	mk := root("install", "-d", "-m", "0711", "-o", "root", "-g", "root", p)
	return hostStep{
		id: idJailerBase, title: "Jailer folder " + p,
		check: func(context.Context) (step.Status, error) {
			info, err := d.FS.Stat(p)
			if errors.Is(err, fs.ErrNotExist) {
				return fail("no " + p)
			}
			if err != nil {
				return step.Status{}, err
			}
			if !info.IsDir() || info.Mode().Perm() != 0o711 {
				return fail(fmt.Sprintf("%s has mode %04o, want a folder with 0711", p, info.Mode().Perm()))
			}
			return pass(p + " ready")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{Cmds: []step.PlannedCmd{mk}, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			existed := d.exists(p)
			if err := d.run(ctx, mk); err != nil {
				return err
			}
			if !existed {
				d.record(step.Item{ID: idJailerBase, Kind: step.KindDir, Path: p, State: step.Created})
			}
			return nil
		},
		undo: func(ctx context.Context, items []step.Item) error {
			var errs []error
			for _, it := range items {
				if it.Kind == step.KindDir {
					// --one-file-system keeps rm out of any bind mount a
					// crashed run left behind.
					errs = append(errs, d.run(ctx, root("rm", "-rf", "--one-file-system", "--", it.Path)))
				}
			}
			return errors.Join(errs...)
		},
	}
}

// --- Rules ---

func (d Deps) rules() step.Step {
	c := d.Config
	rulesFile := d.rendered(c.RulesPath)
	table := "inet " + c.Table
	load := root("nft", "-f", c.RulesPath)
	return hostStep{
		id: idRules, title: "Firewall rules (" + table + ")",
		check: func(ctx context.Context) (step.Status, error) {
			if !d.matches(c.RulesPath, rulesFile.Content) {
				return fail(c.RulesPath + " missing or changed")
			}
			want, err := d.FS.ReadFile(c.HashPath)
			if err != nil {
				return fail("no recorded rule hash at " + c.HashPath)
			}
			live, err := rulehash.Live(ctx, d.Runner, c.Table, true)
			if errors.Is(err, system.ErrLoginExpired) {
				return step.Status{}, err
			}
			if err != nil {
				return fail(table + " not loaded")
			}
			if live != strings.TrimSpace(string(want)) {
				return fail(table + " differs from the rules setup loaded")
			}
			return pass(table + " loaded and unchanged")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{
				Files: []step.PlannedFile{
					rulesFile,
					{Path: c.HashPath, Mode: 0o644, Content: []byte("(the SHA-256 of the table as loaded)\n")},
				},
				Cmds: []step.PlannedCmd{
					root("install", "-d", "-m", "0755", path.Dir(c.RulesPath)),
					root("install", "-d", "-m", "0755", path.Dir(c.HashPath)),
					load,
				},
				NeedsRoot: true,
			}, nil
		},
		apply: func(ctx context.Context) error {
			for _, dir := range []string{path.Dir(c.RulesPath), path.Dir(c.HashPath)} {
				if err := d.ensureDir(ctx, idRules, dir); err != nil {
					return err
				}
			}
			if err := d.writeFile(ctx, rulesFile); err != nil {
				return err
			}
			d.record(step.Item{ID: idRules, Kind: step.KindFile, Path: c.RulesPath, State: step.Created})
			if err := d.run(ctx, load); err != nil {
				return err
			}
			d.record(step.Item{ID: idRules, Kind: step.KindNetwork, Name: table, State: step.Created})
			// Hash the table as loaded here, since nft's JSON can differ
			// between versions.
			live, err := rulehash.Live(ctx, d.Runner, c.Table, true)
			if err != nil {
				return err
			}
			if err := system.WriteRootFile(ctx, d.Runner, c.HashPath, []byte(live+"\n"), 0o644); err != nil {
				return err
			}
			d.record(step.Item{ID: idRules, Kind: step.KindFile, Path: c.HashPath, State: step.Created})
			return nil
		},
		undo: func(ctx context.Context, items []step.Item) error {
			var errs []error
			if hasNetwork(items, table) {
				// The tap service's ExecStop usually deleted the table
				// already; a table already gone counts as removed.
				res, err := d.Runner.Run(ctx, root("nft", "delete", "table", "inet", c.Table))
				if err == nil && res.Code != 0 && !strings.Contains(string(res.Stderr), "No such file or directory") {
					err = fmt.Errorf("nft delete table %s: %s", table, strings.TrimSpace(string(res.Stderr)))
				}
				errs = append(errs, err)
			}
			return errors.Join(append(errs, d.undoFiles(ctx, items))...)
		},
	}
}

// --- NetworkManager ---

func (d Deps) serviceActive(ctx context.Context, unit string) (bool, error) {
	c, err := d.code(ctx, system.Cmd{Name: "systemctl", Args: []string{"is-active", "--quiet", unit}})
	return c == 0, err
}

func (d Deps) nmUnmanaged() step.Step {
	conf := d.rendered(d.Config.NMPath)
	reload := root("systemctl", "reload", "NetworkManager")
	return hostStep{
		id: idNM, title: "NetworkManager leaves the taps alone",
		check: func(ctx context.Context) (step.Status, error) {
			active, err := d.serviceActive(ctx, "NetworkManager")
			if err != nil {
				return step.Status{}, err
			}
			if !active {
				return pass("NetworkManager not running; nothing to do")
			}
			if !d.matches(conf.Path, conf.Content) {
				return fail(conf.Path + " missing or changed")
			}
			return pass("taps unmanaged")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{Files: []step.PlannedFile{conf}, Cmds: []step.PlannedCmd{reload}, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			if err := d.writeFile(ctx, conf); err != nil {
				return err
			}
			d.record(step.Item{ID: idNM, Kind: step.KindFile, Path: conf.Path, State: step.Created})
			return d.run(ctx, reload)
		},
		undo: func(ctx context.Context, items []step.Item) error {
			err := d.undoFiles(ctx, items)
			_, _ = d.code(ctx, reload) // NetworkManager may have gone since
			return err
		},
	}
}

// --- Tap service ---

func (d Deps) tapService() step.Step {
	unitFile := d.rendered(d.Config.UnitPath)
	unit := path.Base(d.Config.UnitPath)
	cmds := []system.Cmd{
		root("systemctl", "daemon-reload"),
		root("systemctl", "enable", unit),
		root("systemctl", "restart", unit),
	}
	return hostStep{
		id: idTap, title: "Tap device service (" + d.Config.Tap + ")",
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
			// A oneshot unit stays active after someone deletes the tap.
			c, err = d.code(ctx, system.Cmd{Name: "ip", Args: []string{"link", "show", "dev", d.Config.Tap}})
			if err != nil {
				return step.Status{}, err
			}
			if c != 0 {
				return fail(d.Config.Tap + " missing although " + unit + " runs")
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
			d.record(step.Item{ID: idTap, Kind: step.KindFile, Path: unitFile.Path, State: step.Created})
			if err := d.relabel(ctx, unitFile.Path); err != nil {
				return err
			}
			if err := d.runAll(ctx, cmds); err != nil {
				return err
			}
			d.record(step.Item{ID: idTap, Kind: step.KindUnit, Name: unit, State: step.Created})
			d.record(step.Item{ID: idTap, Kind: step.KindNetwork, Name: d.Config.Tap, State: step.Created})
			return nil
		},
		undo: func(ctx context.Context, items []step.Item) error {
			var errs []error
			for _, it := range items {
				if it.Kind == step.KindUnit {
					// Stopping runs ExecStop, which removes the tap and table.
					errs = append(errs, d.run(ctx, root("systemctl", "disable", "--now", it.Name)))
				}
			}
			errs = append(errs, d.undoFiles(ctx, items), d.run(ctx, root("systemctl", "daemon-reload")))
			return errors.Join(errs...)
		},
	}
}

// --- firewalld ---

func (d Deps) firewalld() step.Step {
	c := d.Config
	zone := "codecheck"
	port := strconv.Itoa(c.ProxyPort) + "/tcp"
	fc := func(args ...string) system.Cmd { return root("firewall-cmd", args...) }
	cmds := []system.Cmd{
		fc("--permanent", "--zone="+zone, "--change-interface="+c.Tap),
		fc("--permanent", "--zone="+zone, "--add-port="+port),
		fc("--reload"),
	}
	return hostStep{
		id: idFirewalld, title: "firewalld allows the proxy port on " + c.Tap,
		check: func(ctx context.Context) (step.Status, error) {
			active, err := d.serviceActive(ctx, "firewalld")
			if err != nil {
				return step.Status{}, err
			}
			if !active {
				return pass("firewalld not running; nothing to do")
			}
			for _, q := range []string{"--query-interface=" + c.Tap, "--query-port=" + port} {
				code, err := d.code(ctx, fc("--permanent", "--zone="+zone, q))
				if err != nil {
					return step.Status{}, err
				}
				if code != 0 {
					return fail("firewalld zone " + zone + " lacks " + strings.TrimPrefix(q, "--query-"))
				}
			}
			return pass("firewalld zone " + zone + " allows " + port + " on " + c.Tap)
		},
		plan: func(ctx context.Context) (step.Plan, error) {
			// The engine elevates only after planning, so ask without root
			// (polkit usually allows reading); if that fails, list the
			// zone's creation, which Apply skips when the zone exists.
			all := cmds
			q := system.Cmd{Name: "firewall-cmd", Args: []string{"--permanent", "--get-zones"}}
			if res, err := d.Runner.Run(ctx, q); err != nil || res.Code != 0 || !hasWord(res.Stdout, zone) {
				all = append([]system.Cmd{fc("--permanent", "--new-zone="+zone)}, cmds...)
			}
			return step.Plan{Cmds: all, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			res, err := d.Runner.Run(ctx, fc("--permanent", "--get-zones"))
			if err != nil {
				return err
			}
			if res.Code != 0 {
				return fmt.Errorf("firewall-cmd --get-zones: exit %d: %s", res.Code, strings.TrimSpace(string(res.Stderr)))
			}
			if !hasWord(res.Stdout, zone) {
				if err := d.run(ctx, fc("--permanent", "--new-zone="+zone)); err != nil {
					return err
				}
				d.record(step.Item{ID: idFirewalld, Kind: step.KindNetwork, Name: "firewalld zone " + zone, State: step.Created})
			}
			return d.runAll(ctx, cmds)
		},
		undo: func(ctx context.Context, items []step.Item) error {
			if !hasNetwork(items, "firewalld zone "+zone) {
				return nil
			}
			if err := d.run(ctx, fc("--permanent", "--delete-zone="+zone)); err != nil {
				return err
			}
			// The permanent change stands; a stopped firewalld needs no reload.
			if active, err := d.serviceActive(ctx, "firewalld"); err != nil || !active {
				return err
			}
			return d.run(ctx, fc("--reload"))
		},
	}
}

// hasWord reports whether out lists word among its space-separated words.
func hasWord(out []byte, word string) bool {
	for _, w := range strings.Fields(string(out)) {
		if w == word {
			return true
		}
	}
	return false
}

// --- ufw ---

func (d Deps) ufwEnabled() bool {
	data, err := d.FS.ReadFile("/etc/ufw/ufw.conf")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(data)) {
		if strings.TrimSpace(line) == "ENABLED=yes" {
			return true
		}
	}
	return false
}

func (d Deps) ufw() step.Step {
	c := d.Config
	rule := []string{"allow", "in", "on", c.Tap, "to", c.HostIP, "port", strconv.Itoa(c.ProxyPort), "proto", "tcp"}
	add := root("ufw", append(rule, "comment", "codecheck-sandbox")...)
	name := "ufw allow " + c.Tap + " " + strconv.Itoa(c.ProxyPort)
	return hostStep{
		id: idUfw, title: "ufw allows the proxy port on " + c.Tap,
		check: func(ctx context.Context) (step.Status, error) {
			if !d.ufwEnabled() {
				return pass("ufw not enabled; nothing to do")
			}
			res, err := d.Runner.Run(ctx, root("ufw", "show", "added"))
			if err != nil {
				return step.Status{}, err
			}
			if res.Code != 0 {
				return step.Status{}, fmt.Errorf("ufw show added: exit %d: %s", res.Code, strings.TrimSpace(string(res.Stderr)))
			}
			if !strings.Contains(string(res.Stdout), "ufw "+strings.Join(rule, " ")) {
				return fail("ufw lacks the rule for " + c.Tap)
			}
			return pass("ufw allows the proxy port")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{Cmds: []step.PlannedCmd{add}, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			if err := d.run(ctx, add); err != nil {
				return err
			}
			d.record(step.Item{ID: idUfw, Kind: step.KindNetwork, Name: name, State: step.Created})
			return nil
		},
		undo: func(ctx context.Context, items []step.Item) error {
			if !hasNetwork(items, name) {
				return nil
			}
			return d.run(ctx, root("ufw", append([]string{"delete"}, rule...)...))
		},
	}
}

// --- SELinux ---

func (d Deps) selinux() step.Step {
	c := d.Config
	candidates := []string{path.Dir(c.RulesPath), path.Dir(c.HashPath), c.JailerBase, "/usr/local/lib/codecheck-sandbox", c.UnitPath, c.ProxyUnitPath}
	present := func() []string {
		var out []string
		for _, p := range candidates {
			if d.exists(p) {
				out = append(out, p)
			}
		}
		return out
	}
	return hostStep{
		id: idSELinux, title: "SELinux labels",
		check: func(ctx context.Context) (step.Status, error) {
			if !d.selinuxEnforcing() {
				return pass("SELinux not enforcing; nothing to do")
			}
			paths := present()
			if len(paths) == 0 {
				return pass("nothing to label yet")
			}
			res, err := d.Runner.Run(ctx, root("restorecon", append([]string{"-RnvF", "--"}, paths...)...))
			if err != nil {
				return step.Status{}, err
			}
			if res.Code != 0 {
				return step.Status{}, fmt.Errorf("restorecon dry run: exit %d: %s", res.Code, strings.TrimSpace(string(res.Stderr)))
			}
			if strings.TrimSpace(string(res.Stdout)) != "" {
				return fail("files need their default SELinux labels")
			}
			return pass("labels match the policy")
		},
		plan: func(context.Context) (step.Plan, error) {
			return step.Plan{Cmds: []step.PlannedCmd{root("restorecon", append([]string{"-RF", "--"}, present()...)...)}, NeedsRoot: true}, nil
		},
		apply: func(ctx context.Context) error {
			return d.run(ctx, root("restorecon", append([]string{"-RF", "--"}, present()...)...))
		},
		undo: func(context.Context, []step.Item) error { return nil }, // labels leave with the files
	}
}
