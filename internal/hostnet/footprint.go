package hostnet

import (
	"path/filepath"

	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// Footprint lists everything the host steps can create, each under the step
// that removes it, for uninstall's fallback when the manifest has gone
// missing. Folders come last within each step, and their removal leaves a
// folder that still holds anything (rmdir --ignore-fail-on-non-empty).
// firewalld and ufw allowances stay out: without the manifest nothing says
// whether setup added them or found them.
func Footprint(c Config) []step.Item {
	file := func(id, p string) step.Item {
		return step.Item{ID: id, Kind: step.KindFile, Path: p, State: step.Created}
	}
	dir := func(id, p string) step.Item {
		return step.Item{ID: id, Kind: step.KindDir, Path: p, State: step.Created}
	}
	named := func(id string, k step.ItemKind, n string) step.Item {
		return step.Item{ID: id, Kind: k, Name: n, State: step.Created}
	}
	return []step.Item{
		named(idJailUser, step.KindUser, c.JailUser),
		dir(idJailerBase, c.JailerBase),
		file(idRules, c.RulesPath),
		file(idRules, c.HashPath),
		named(idRules, step.KindNetwork, "inet "+c.Table),
		dir(idRules, filepath.Dir(c.RulesPath)),
		dir(idRules, filepath.Dir(c.HashPath)),
		file(idNM, c.NMPath),
		file(idTap, c.UnitPath),
		named(idTap, step.KindUnit, filepath.Base(c.UnitPath)),
		named(idTap, step.KindNetwork, c.Tap),
		file(idProxyBinary, c.ProxyBinary),
		dir(idProxyBinary, filepath.Dir(c.ProxyBinary)),
		file(idProxyCA, c.CAKeyPath),
		file(idProxyCA, c.CACertPath),
		dir(idProxyCA, filepath.Dir(c.CACertPath)),
		file(idAPIKey, c.APIKeyPath),
		file(idProxyService, c.ProxyUnitPath),
		named(idProxyService, step.KindUnit, filepath.Base(c.ProxyUnitPath)),
		file(idLauncher, launcherPath),
		file(idLock, tmpfilesPath),
		dir(idLock, lockDir),
		file(idSudoers, sudoersPath),
	}
}
