package plugin

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestFindLocalFlynnLayer(t *testing.T) {
	dir := t.TempDir()
	id := "oslayerid"
	path := filepath.Join(dir, id+".squashfs")
	if err := os.WriteFile(path, []byte("squashfs"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findLocalFlynnLayer(id, []string{dir}); got != path {
		t.Fatalf("got %q", got)
	}
	if findLocalFlynnLayer("missing", []string{dir}) != "" {
		t.Fatal("missing id")
	}
	if findLocalFlynnLayer("", []string{dir}) != "" {
		t.Fatal("empty id")
	}
}

func TestLocalImagesJSONAndEnv(t *testing.T) {
	t.Setenv(EnvImagesJSON, "")
	t.Setenv(EnvLayersDir, "")
	if localImagesJSONPath() != "" && strings.HasPrefix(localImagesJSONPath(), t.TempDir()) {
		t.Fatal("must not invent a temp images.json")
	}

	img := filepath.Join(t.TempDir(), "images.json")
	if err := os.WriteFile(img, []byte(`{"ubuntu-noble":{"type":"flynn"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvImagesJSON, img)
	if localImagesJSONPath() != img {
		t.Fatalf("got %q", localImagesJSONPath())
	}
	layers := t.TempDir()
	t.Setenv(EnvLayersDir, layers)
	env := LocalFlynnImageEnv()
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, EnvImagesJSON+"="+img) || !strings.Contains(joined, EnvLayersDir+"="+layers) {
		t.Fatalf("env=%v", env)
	}
}

func TestImagesJSONHasLayer(t *testing.T) {
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{ID: id, Type: ct.ImageLayerTypeSquashfs, Length: 40 << 20}},
		}},
	}
	art := &ct.Artifact{Type: ct.ArtifactTypeFlynn, RawManifest: manifest.RawManifest()}
	path := filepath.Join(t.TempDir(), "images.json")
	writeJSON(t, path, map[string]*ct.Artifact{"ubuntu-noble": art})
	if !imagesJSONHasLayer(path, id) {
		t.Fatal("images.json must list the OS layer")
	}
	if imagesJSONHasLayer(path, "missing") {
		t.Fatal("unknown layer")
	}
	if artifactsHaveLayer([]*ct.Artifact{art}, id) != true {
		t.Fatal("artifact list")
	}
}

func TestLinkOrCopyFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("layer"), 0644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "nested", "dst.bin")
	if err := linkOrCopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "layer" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestInstallerLocalFlynnLayerUsesCacheDir(t *testing.T) {
	dir := t.TempDir()
	id := "cached-os"
	if err := os.WriteFile(filepath.Join(dir, id+".squashfs"), []byte("os"), 0644); err != nil {
		t.Fatal(err)
	}
	in := &Installer{LayerCacheDir: dir}
	if got := in.localFlynnLayer(id); got == "" {
		t.Fatal("expected cached layer")
	}
	var buf bytes.Buffer
	in.Stdout = &buf
	env := in.localFlynnImageEnv()
	if !strings.Contains(strings.Join(env, "\n"), EnvLayersDir+"="+dir) {
		t.Fatalf("build env=%v", env)
	}
}
