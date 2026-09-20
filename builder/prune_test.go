package main

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func writeLayerPair(t *testing.T, dir, id string, mtime time.Time) {
	t.Helper()
	for _, ext := range []string{".squashfs", ".json"} {
		p := filepath.Join(dir, id+ext)
		if err := os.WriteFile(p, []byte(id), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruneLayerCache(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	writeLayerPair(t, dir, "referenced-old", old)
	writeLayerPair(t, dir, "unreferenced-old", old)
	writeLayerPair(t, dir, "unreferenced-fresh", now)
	// A hit touched only the squashfs: the newest file of the pair counts.
	writeLayerPair(t, dir, "half-touched", old)
	if err := os.Chtimes(filepath.Join(dir, "half-touched.squashfs"), now, now); err != nil {
		t.Fatal(err)
	}
	// An orphaned sidecar with no squashfs is pruned like any other stale id.
	orphan := filepath.Join(dir, "orphan.json")
	if err := os.WriteFile(orphan, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	// Unrelated files are never touched.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	referenced := map[string]struct{}{"referenced-old": {}}
	cutoff := now.Add(-7 * 24 * time.Hour)

	removed, kept, err := pruneLayerCache(dir, referenced, cutoff, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"orphan", "unreferenced-old"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("dry-run removed = %v, want %v", removed, want)
	}
	if kept != 3 {
		t.Fatalf("dry-run kept = %d, want 3", kept)
	}
	if _, err := os.Stat(filepath.Join(dir, "unreferenced-old.squashfs")); err != nil {
		t.Fatal("dry run must not delete anything")
	}

	removed, _, err = pruneLayerCache(dir, referenced, cutoff, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v", removed)
	}
	for _, gone := range []string{"unreferenced-old.squashfs", "unreferenced-old.json", "orphan.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s should have been removed (err=%v)", gone, err)
		}
	}
	for _, stay := range []string{"referenced-old.squashfs", "referenced-old.json", "unreferenced-fresh.squashfs", "half-touched.json", "README"} {
		if _, err := os.Stat(filepath.Join(dir, stay)); err != nil {
			t.Fatalf("%s should have been kept: %v", stay, err)
		}
	}

	if removed, kept, err := pruneLayerCache(filepath.Join(dir, "missing"), nil, cutoff, false); err != nil || removed != nil || kept != 0 {
		t.Fatalf("missing dir = %v,%d,%v; want no-op", removed, kept, err)
	}
}

func TestReferencedLayerIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "images.json")
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{ID: "layer-a"}, {ID: "layer-b"}},
		}},
	}
	artifacts := map[string]*ct.Artifact{
		"controller":  {Type: ct.ArtifactTypeFlynn, RawManifest: manifest.RawManifest()},
		"no-manifest": {Type: ct.ArtifactTypeFlynn},
	}
	if err := writeArtifacts(path, artifacts); err != nil {
		t.Fatal(err)
	}
	ids, err := referencedLayerIDs(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{}{"layer-a": {}, "layer-b": {}}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}

	ids, err = referencedLayerIDs(filepath.Join(dir, "absent.json"))
	if err != nil || len(ids) != 0 {
		t.Fatalf("absent images.json = %v,%v; want empty, nil", ids, err)
	}
}

func TestCachedLayerFromFilesVerifiesDigest(t *testing.T) {
	dir := t.TempDir()
	id := "digest"
	squashfs := filepath.Join(dir, id+".squashfs")
	config := filepath.Join(dir, id+".json")
	blob := []byte("squashfs bytes")
	sum := sha512.Sum512_256(blob)
	layer := &ct.ImageLayer{
		ID:     id,
		Length: int64(len(blob)),
		Type:   ct.ImageLayerTypeSquashfs,
		Hashes: map[string]string{"sha512_256": hex.EncodeToString(sum[:])},
	}
	raw, err := json.Marshal(layer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(squashfs, blob, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := cachedLayerFromFiles(squashfs, config)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("matching digest must hit, got %+v", got)
	}

	// Same length, different bytes: a persisted-but-corrupt blob is a miss.
	if err := os.WriteFile(squashfs, []byte("squashfs BYTES"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = cachedLayerFromFiles(squashfs, config)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("digest mismatch must be a miss, got %+v", got)
	}
}

func TestGetCachedLayerTouchesFiles(t *testing.T) {
	dir := t.TempDir()
	b := quietBuilder()
	b.layerCacheDir = dir
	id := "touched"
	old := time.Now().Add(-48 * time.Hour)
	blob := []byte("layer")
	layer := &ct.ImageLayer{ID: id, Length: int64(len(blob)), Type: ct.ImageLayerTypeSquashfs}
	raw, _ := json.Marshal(layer)
	if err := os.WriteFile(b.layerConfigPath(id), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.layerPath(id), blob, 0644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{b.layerPath(id), b.layerConfigPath(id)} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	got, err := b.GetCachedLayer("img", id)
	if err != nil || got == nil {
		t.Fatalf("GetCachedLayer = %+v, %v", got, err)
	}
	for _, p := range []string{b.layerPath(id), b.layerConfigPath(id)} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !st.ModTime().After(old.Add(time.Hour)) {
			t.Fatalf("%s mtime %s was not refreshed on cache hit", p, st.ModTime())
		}
	}
}
