package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	DistDir   = "dist"
	ImageJSON = "image.json"

	// Plugin images are Flynn ubuntu-noble plus a delta. A single ~35MiB
	// squashfs is the overlay only (no glibc/bash); installing it makes
	// scale hang until DefaultScaleTimeout. Busybox (blobstore after
	// image-slim) is ~2MiB, so reject a first layer smaller than this.
	MinPluginRootfsLayers = 2
	MinOSRootfsBytes      = 32 << 20
)

// DistArtifact is a local plugin-build output under dist/.
type DistArtifact struct {
	Artifact *ct.Artifact
	Root     string // plugin checkout
	Dist     string // <root>/dist
}

// DistReady reports whether dist/image.json and every referenced squashfs exist.
func DistReady(root string) bool {
	_, err := LoadDist(root)
	return err == nil
}

func LoadDist(root string) (*DistArtifact, error) {
	dist := filepath.Join(root, DistDir)
	path := filepath.Join(dist, ImageJSON)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plugin image not built (%s missing); run script/plugin-build", path)
	}
	art := &ct.Artifact{}
	if err := json.Unmarshal(data, art); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if art.Type == "" {
		art.Type = ct.ArtifactTypeFlynn
	}
	if len(art.RawManifest) == 0 {
		return nil, fmt.Errorf("%s: missing manifest", path)
	}
	if err := ValidatePluginLayers(art); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, layer := range layers(art) {
		if findLayerFile(dist, layer.ID) == "" {
			return nil, fmt.Errorf("missing layer %s under %s/layers (run script/plugin-build)", layer.ID, dist)
		}
	}
	return &DistArtifact{Artifact: art, Root: root, Dist: dist}, nil
}

// ValidatePluginLayers rejects delta-only or busybox plugin images that cannot
// boot a Flynn job.
func ValidatePluginLayers(art *ct.Artifact) error {
	if art == nil {
		return fmt.Errorf("missing artifact")
	}
	ls := layers(art)
	if len(ls) < MinPluginRootfsLayers {
		return fmt.Errorf("plugin image has %d layer(s); need Flynn ubuntu-noble plus a plugin delta (rebuild with current script/plugin-build)", len(ls))
	}
	if ls[0] == nil || ls[0].Length < MinOSRootfsBytes {
		n := int64(0)
		if ls[0] != nil {
			n = ls[0].Length
		}
		return fmt.Errorf("plugin image layer 0 is %d bytes; expected Flynn ubuntu-noble (>= %d). blobstore after image-slim is busybox — rebuild with current plugin-build", n, int64(MinOSRootfsBytes))
	}
	return validatePluginArch(art, runtime.GOOS, runtime.GOARCH)
}

func validatePluginArch(art *ct.Artifact, goos, goarch string) error {
	if goos != "linux" || art == nil || art.Meta == nil {
		return nil
	}
	arch := art.Meta["flynn.plugin.arch"]
	if arch == "" || arch == goarch {
		return nil
	}
	return fmt.Errorf("plugin image is linux/%s; this Flynn host is linux/%s (rebuild the plugin for this architecture)", arch, goarch)
}

func layers(art *ct.Artifact) []*ct.ImageLayer {
	var out []*ct.ImageLayer
	for _, rootfs := range art.Manifest().Rootfs {
		out = append(out, rootfs.Layers...)
	}
	return out
}

func findLayerFile(dist, id string) string {
	candidates := []string{
		filepath.Join(dist, "layers", id+".squashfs"),
		filepath.Join(dist, id+".squashfs"),
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func (d *DistArtifact) LayerPath(id string) string {
	return findLayerFile(d.Dist, id)
}

func (d *DistArtifact) ManifestPath() string {
	id := d.Artifact.Manifest().ID()
	if id == "" && d.Artifact.Hashes != nil {
		id = d.Artifact.Hashes["sha512_256"]
	}
	p := filepath.Join(d.Dist, id+".json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}
