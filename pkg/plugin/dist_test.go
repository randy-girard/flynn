package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestLoadDistRejectsDeltaOnlyImage(t *testing.T) {
	root := t.TempDir()
	writePluginDist(t, root, []*ct.ImageLayer{{
		ID:     "delta-only",
		Type:   ct.ImageLayerTypeSquashfs,
		Length: 35397632, // redis overlay from a pre-stack plugin-build
		Hashes: map[string]string{"sha512_256": "delta-only"},
	}})
	_, err := LoadDist(root)
	if err == nil {
		t.Fatal("delta-only plugin image must fail (no ubuntu-noble)")
	}
	if !strings.Contains(err.Error(), "ubuntu-noble plus a plugin delta") {
		t.Fatalf("got %v", err)
	}
	if DistReady(root) {
		t.Fatal("DistReady must be false for a 1-layer overlay")
	}
}

func TestLoadDistRejectsBusyboxOSLayer(t *testing.T) {
	root := t.TempDir()
	writePluginDist(t, root, []*ct.ImageLayer{
		{
			ID:     "busybox",
			Type:   ct.ImageLayerTypeSquashfs,
			Length: 1 << 20,
			Hashes: map[string]string{"sha512_256": "busybox"},
		},
		{
			ID:     "delta",
			Type:   ct.ImageLayerTypeSquashfs,
			Length: 10 << 20,
			Hashes: map[string]string{"sha512_256": "delta"},
		},
	})
	_, err := LoadDist(root)
	if err == nil {
		t.Fatal("busybox layer 0 must fail")
	}
	if !strings.Contains(err.Error(), "ubuntu-noble") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadDistAcceptsStackedUbuntuNoble(t *testing.T) {
	root := t.TempDir()
	writePluginDist(t, root, stackedUbuntuLayers())
	dist, err := LoadDist(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := layers(dist.Artifact); len(got) != 2 {
		t.Fatalf("layers=%d", len(got))
	}
	if !DistReady(root) {
		t.Fatal("stacked image must be DistReady")
	}
}

func TestValidatePluginLayersNil(t *testing.T) {
	if err := ValidatePluginLayers(nil); err == nil {
		t.Fatal("nil artifact must fail")
	}
}

func TestValidatePluginArchMismatch(t *testing.T) {
	art := &ct.Artifact{Meta: map[string]string{"flynn.plugin.arch": "amd64"}}
	err := validatePluginArch(art, "linux", "arm64")
	if err == nil || !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("got %v", err)
	}
	if err := validatePluginArch(art, "darwin", "arm64"); err != nil {
		t.Fatalf("non-linux hosts must not check arch: %v", err)
	}
	if err := validatePluginArch(art, "linux", "amd64"); err != nil {
		t.Fatalf("matching arch must pass: %v", err)
	}
}

func TestValidatePluginArchEmptyMeta(t *testing.T) {
	if err := validatePluginArch(nil, "linux", "arm64"); err != nil {
		t.Fatalf("nil artifact: %v", err)
	}
	if err := validatePluginArch(&ct.Artifact{}, "linux", "arm64"); err != nil {
		t.Fatalf("missing meta: %v", err)
	}
	art := &ct.Artifact{Meta: map[string]string{"flynn.plugin.arch": ""}}
	if err := validatePluginArch(art, "linux", "arm64"); err != nil {
		t.Fatalf("empty arch: %v", err)
	}
}

func TestLoadDistEnforcesArchOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("flynn-host only rejects plugin arch on Linux")
	}
	root := t.TempDir()
	wrong := "amd64"
	if runtime.GOARCH == "amd64" {
		wrong = "arm64"
	}
	writePluginDistMeta(t, root, stackedUbuntuLayers(), map[string]string{"flynn.plugin.arch": wrong})
	_, err := LoadDist(root)
	if err == nil || !strings.Contains(err.Error(), "rebuild the plugin") {
		t.Fatalf("got %v", err)
	}
	if DistReady(root) {
		t.Fatal("wrong-arch dist must not be DistReady on Linux")
	}

	root = t.TempDir()
	writePluginDistMeta(t, root, stackedUbuntuLayers(), map[string]string{"flynn.plugin.arch": runtime.GOARCH})
	if _, err := LoadDist(root); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDistMissingImageAndLayer(t *testing.T) {
	root := t.TempDir()
	if DistReady(root) {
		t.Fatal("empty plugin must not be DistReady")
	}
	if _, err := LoadDist(root); err == nil || !strings.Contains(err.Error(), "plugin-build") {
		t.Fatalf("missing image.json: %v", err)
	}

	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, DistDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DistDir, ImageJSON), []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDist(root); err == nil {
		t.Fatal("invalid image.json must fail")
	}

	root = t.TempDir()
	writePluginDist(t, root, stackedUbuntuLayers())
	if err := os.Remove(filepath.Join(root, DistDir, "layers", "plugin-delta.squashfs")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDist(root); err == nil || !strings.Contains(err.Error(), "missing layer") {
		t.Fatalf("missing squashfs: %v", err)
	}
}

func TestDistLayerAndManifestPaths(t *testing.T) {
	root := t.TempDir()
	writePluginDist(t, root, stackedUbuntuLayers())
	dist, err := LoadDist(root)
	if err != nil {
		t.Fatal(err)
	}
	if dist.LayerPath("ubuntu-noble") == "" || dist.LayerPath("missing") != "" {
		t.Fatalf("layers dir lookup: %q", dist.LayerPath("ubuntu-noble"))
	}
	if err := os.Remove(filepath.Join(dist.Dist, "layers", "ubuntu-noble.squashfs")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist.Dist, "ubuntu-noble.squashfs"), []byte("squashfs"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := dist.LayerPath("ubuntu-noble"); got == "" || !strings.HasSuffix(got, "ubuntu-noble.squashfs") {
		t.Fatalf("dist-root layer: %q", got)
	}
	if dist.ManifestPath() != "" {
		t.Fatalf("missing sidecar json must be empty, got %s", dist.ManifestPath())
	}
	id := dist.Artifact.Manifest().ID()
	if id == "" && dist.Artifact.Hashes != nil {
		id = dist.Artifact.Hashes["sha512_256"]
	}
	if id == "" {
		t.Fatal("expected manifest id")
	}
	sidecar := filepath.Join(dist.Dist, id+".json")
	if err := os.WriteFile(sidecar, dist.Artifact.RawManifest, 0644); err != nil {
		t.Fatal(err)
	}
	if dist.ManifestPath() != sidecar {
		t.Fatalf("ManifestPath=%s want %s", dist.ManifestPath(), sidecar)
	}
}

func stackedUbuntuLayers() []*ct.ImageLayer {
	return []*ct.ImageLayer{
		{
			ID:     "ubuntu-noble",
			Type:   ct.ImageLayerTypeSquashfs,
			Length: 199 << 20,
			Hashes: map[string]string{"sha512_256": "ubuntu-noble"},
		},
		{
			ID:     "plugin-delta",
			Type:   ct.ImageLayerTypeSquashfs,
			Length: 34 << 20,
			Hashes: map[string]string{"sha512_256": "plugin-delta"},
		},
	}
}

func writePluginDist(t *testing.T, root string, ls []*ct.ImageLayer) {
	t.Helper()
	writePluginDistMeta(t, root, ls, nil)
}

func writePluginDistMeta(t *testing.T, root string, ls []*ct.ImageLayer, meta map[string]string) {
	t.Helper()
	dist := filepath.Join(root, DistDir)
	if err := os.MkdirAll(filepath.Join(dist, "layers"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := &ct.ImageManifest{
		Type:   ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{Layers: ls}},
	}
	raw := manifest.RawManifest()
	art := &ct.Artifact{
		Type:        ct.ArtifactTypeFlynn,
		RawManifest: raw,
		Hashes:      manifest.Hashes(),
		Size:        int64(len(raw)),
		Meta:        meta,
	}
	data, err := json.Marshal(art)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, ImageJSON), data, 0644); err != nil {
		t.Fatal(err)
	}
	for _, layer := range ls {
		p := filepath.Join(dist, "layers", layer.ID+".squashfs")
		if err := os.WriteFile(p, []byte("squashfs"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
