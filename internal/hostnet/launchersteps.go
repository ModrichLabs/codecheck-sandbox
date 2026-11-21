package hostnet

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"text/template"

	"github.com/ModrichLabs/codecheck-sandbox/assets"
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

const (
	idLauncher = "host-launcher"
	idLock     = "host-lock"
	idSudoers  = "host-sudoers"
)

// The launcher's footprint. cc-launch's own paths come from the launch
// package, so the two can't drift apart.
var (
	launcherPath = filepath.Join(launch.DefaultPaths().BinDir, "cc-launch")
	lockPath     = launch.DefaultPaths().LockPath
	lockDir      = path.Dir(lockPath)
)

const (
	tmpfilesPath = "/etc/tmpfiles.d/codecheck-sandbox.conf"
	sudoersPath  = "/etc/sudoers.d/codecheck-sandbox"
	// sudo skips drop-ins whose names hold a dot, so a draft left behind
	// after a failure grants nothing.
	sudoersDraft = sudoersPath + ".new"
)

// renderHostAsset fills one of the launcher's templates.
func renderHostAsset(name string, data any) ([]byte, error) {
	t, err := template.New(name).Option("missingkey=error").ParseFS(assets.Host, "host/"+name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// --- Launcher binary ---

// launcher installs cc-launch, which ships beside the CLI binary, root-owned:
// sudo runs it as root without a password, so no user may rewrite it.
func (d Deps) launcher() step.Step {
	source := func() (string, error) {
		self, err := d.self()
		if err != nil {
			return "", err
		}
		return filepath.Join(filepath.Dir(self), "cc-launch"), nil
	}
	return d.binaryStep(idLauncher, "Launch helper ", launcherPath, source, nil)
}

// --- Lock ---

// lock installs a tmpfiles.d entry for cc-launch's lock folder and file, and
// creates them now; /run empties at every boot. A lock the user created
// would stop root from opening it under fs.protected_regular (M3).
func (d Deps) lock() step.Step {
	file := func() (step.PlannedFile, error) {
		b, err := renderHostAsset("tmpfiles.conf.tmpl", struct{ LockDir, LockPath string }{lockDir, lockPath})
		return step.PlannedFile{Path: tmpfilesPath, Mode: 0o644, Content: b}, err
	}
	create := root("systemd-tmpfiles", "--create", tmpfilesPath)
	return hostStep{
		id: idLock, title: "Launch lock " + lockPath,
		check: func(context.Context) (step.Status, error) {
			f, err := file()
			if err != nil {
				return step.Status{}, err
			}
			if !d.matches(tmpfilesPath, f.Content) {
				return fail(tmpfilesPath + " missing or changed")
			}
			if !d.exists(lockPath) {
				return fail("no " + lockPath)
			}
			return pass(lockPath + " in place")
		},
		plan: func(context.Context) (step.Plan, error) {
			f, err := file()
			return step.Plan{Files: []step.PlannedFile{f}, Cmds: []step.PlannedCmd{create}, NeedsRoot: true}, err
		},
		apply: func(ctx context.Context) error {
			f, err := file()
			if err != nil {
				return err
			}
			existed := map[string]bool{tmpfilesPath: d.exists(tmpfilesPath), lockDir: d.exists(lockDir), lockPath: d.exists(lockPath)}
			if err := d.writeFile(ctx, f); err != nil {
				return err
			}
			if err := d.run(ctx, create); err != nil {
				return err
			}
			for _, it := range []step.Item{
				{ID: idLock, Kind: step.KindFile, Path: tmpfilesPath, State: step.Created},
				{ID: idLock, Kind: step.KindDir, Path: lockDir, State: step.Created},
				{ID: idLock, Kind: step.KindFile, Path: lockPath, State: step.Created},
			} {
				if !existed[it.Path] {
					d.record(it)
				}
			}
			return nil
		},
		undo: d.undoFiles,
	}
}

// --- Sudoers ---

// sudoers lets the user run cc-launch, and nothing else, as root without a
// password. cc-launch takes only a run id, so the rule needs no argument
// pattern. The drop-in passes visudo before it moves into place.
func (d Deps) sudoers() step.Step {
	file := func() (step.PlannedFile, error) {
		if !userPattern.MatchString(d.User) || d.User == "root" {
			return step.PlannedFile{}, fmt.Errorf("user %q: want a plain, unprivileged account name", d.User)
		}
		b, err := renderHostAsset("sudoers.tmpl", struct{ User, Launcher string }{d.User, launcherPath})
		return step.PlannedFile{Path: sudoersPath, Mode: 0o440, Content: b}, err
	}
	return hostStep{
		id: idSudoers, title: "sudo rule for cc-launch",
		check: func(context.Context) (step.Status, error) {
			f, err := file()
			if err != nil {
				return step.Status{}, err
			}
			if !d.matches(sudoersPath, f.Content) {
				return fail(sudoersPath + " missing or changed")
			}
			return pass(sudoersPath + " in place")
		},
		plan: func(context.Context) (step.Plan, error) {
			f, err := file()
			return step.Plan{Files: []step.PlannedFile{f}, Cmds: []step.PlannedCmd{
				root("visudo", "-cf", sudoersDraft),
				root("mv", "-f", "-T", sudoersDraft, sudoersPath),
			}, NeedsRoot: true}, err
		},
		apply: func(ctx context.Context) error {
			f, err := file()
			if err != nil {
				return err
			}
			existed := d.exists(sudoersPath)
			draft := f
			draft.Path = sudoersDraft
			if err := d.writeFile(ctx, draft); err != nil {
				return err
			}
			if err := d.run(ctx, root("visudo", "-cf", sudoersDraft)); err != nil {
				_ = d.run(ctx, root("rm", "-f", "--", sudoersDraft))
				return fmt.Errorf("visudo refused the drop-in: %w", err)
			}
			if err := d.run(ctx, root("mv", "-f", "-T", sudoersDraft, sudoersPath)); err != nil {
				return err
			}
			if !existed {
				d.record(step.Item{ID: idSudoers, Kind: step.KindFile, Path: sudoersPath, State: step.Created})
			}
			return nil
		},
		undo: d.undoFiles,
	}
}
