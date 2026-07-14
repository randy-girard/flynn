package downloader

import (
	"fmt"
	"os"
	"path/filepath"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/ghrelease"
	"github.com/inconshreveable/log15"
)

const DefaultGitHubRepo = "randy-girard/flynn"

// EnsureLayerInCache downloads a squashfs layer into cacheDir when it is
// missing or truncated. Mountspecs reference file:///var/lib/flynn/layer-cache
// paths; if cleanup or manual deletion removed a layer, jobs can restore it
// from the release assets using hashes embedded in the mountspec.
func EnsureLayerInCache(id string, length int64, hashes map[string]string, cacheDir, repo, version string, log log15.Logger) error {
	if id == "" {
		return fmt.Errorf("layer id is required")
	}
	if len(hashes) == 0 || length <= 0 {
		return fmt.Errorf("layer %s has no verification metadata for download", id)
	}
	if repo == "" {
		repo = DefaultGitHubRepo
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("error creating layer cache dir: %s", err)
	}
	layerPath := filepath.Join(cacheDir, id+".squashfs")
	if fi, err := os.Stat(layerPath); err == nil {
		if fi.Size() == length {
			return verifyLayerFile(layerPath, length, hashes)
		}
		log.Warn("cached layer has wrong size, re-downloading", "layer", id, "expected", length, "actual", fi.Size())
		os.Remove(layerPath)
	}
	d := &Downloader{
		client:  ghrelease.NewClient(repo, log),
		repo:    repo,
		version: version,
		log:     log,
	}
	layer := &ct.ImageLayer{
		ID:     id,
		Length: length,
		Hashes: hashes,
	}
	return d.downloadLayer(layer, cacheDir)
}
