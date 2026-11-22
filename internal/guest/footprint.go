package guest

import (
	"github.com/ModrichLabs/codecheck-sandbox/internal/launch"
	"github.com/ModrichLabs/codecheck-sandbox/internal/step"
)

// Footprint lists everything the guest steps can create, each under the
// step that removes it, for uninstall's fallback when the manifest has gone
// missing.
func Footprint(p launch.Paths) []step.Item {
	file := func(id, path string) step.Item {
		return step.Item{ID: id, Kind: step.KindFile, Path: path, State: step.Created}
	}
	return []step.Item{
		file(idFirecracker, p.Firecracker),
		file(idFirecracker, p.Jailer),
		{ID: idFirecracker, Kind: step.KindDir, Path: p.BinDir, State: step.Created},
		file(idImage, p.Kernel),
		file(idImage, p.RootDisk),
		file(idImage, p.ImagesSum),
		file(idImage, StampPath(p)),
	}
}
