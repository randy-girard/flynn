package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestCachedLayerFromFilesRequiresSquashfs(t *testing.T) {
	dir := t.TempDir()
	id := "abc123"
	squashfs := filepath.Join(dir, id+".squashfs")
	config := filepath.Join(dir, id+".json")
	layer := &ct.ImageLayer{ID: id, Length: 4096, Type: ct.ImageLayerTypeSquashfs}
	raw, err := json.Marshal(layer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, raw, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := cachedLayerFromFiles(squashfs, config)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("json-only cache must be a miss, got %+v", got)
	}

	if err := os.WriteFile(squashfs, make([]byte, 4096), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = cachedLayerFromFiles(squashfs, config)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != id || got.Length != 4096 {
		t.Fatalf("complete cache hit = %+v", got)
	}

	if err := os.WriteFile(squashfs, make([]byte, 8), 0644); err != nil {
		t.Fatal(err)
	}
	got, err = cachedLayerFromFiles(squashfs, config)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("size mismatch must be a miss, got %+v", got)
	}
}
