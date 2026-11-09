package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

// A previous artifact whose squashfs is still on disk must not make BuildImage
// return the old image after an input changes. Layer identity is the input
// hash; GetCachedLayer reuses a blob only when that hash still matches.
func TestBuildImageRebuildsWhenCachedArtifactInputsChange(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	const oldID = "stale-layer-id"
	cache := filepath.Join(dir, "layer-cache")
	if err := os.MkdirAll(cache, 0755); err != nil {
		t.Fatal(err)
	}
	old := &ct.ImageLayer{ID: oldID, Type: ct.ImageLayerTypeSquashfs, Length: 4}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, oldID+".json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, oldID+".squashfs"), []byte("old!"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{old},
		}},
	}
	stale := &ct.Artifact{Type: ct.ArtifactTypeFlynn, RawManifest: manifest.RawManifest()}

	if err := os.WriteFile("layer.sh", []byte("echo rebuilt\n"), 0644); err != nil {
		t.Fatal(err)
	}

	b := quietBuilder()
	b.layerCacheDir = cache
	b.fileCache = map[string]*fileInput{}
	b.artifacts = map[string]*ct.Artifact{"cache-app": stale}
	if !b.artifactLayersCached(stale) {
		t.Fatal("fixture layers are not on disk; the cached-artifact skip would not have applied")
	}

	var built string
	b.buildLayerHook = func(id string) (*ct.ImageLayer, error) {
		built = id
		return &ct.ImageLayer{ID: id, Type: ct.ImageLayerTypeSquashfs, Length: 1}, nil
	}

	image := &Image{
		ID: "cache-app",
		Layers: []*Layer{{
			Script: "layer.sh",
		}},
	}
	if err := b.BuildImage(image); err != nil {
		t.Fatal(err)
	}
	if built == "" || built == oldID {
		t.Fatalf("BuildImage reused artifact layer %q (hook id %q)", oldID, built)
	}
	got := b.artifacts["cache-app"].Manifest()
	if got == nil || len(got.Rootfs) != 1 || len(got.Rootfs[0].Layers) != 1 || got.Rootfs[0].Layers[0].ID != built {
		t.Fatalf("stored artifact = %#v, want layer %s", got, built)
	}
}
