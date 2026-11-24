package dockerimage

import (
	"archive/tar"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// EDGE-106: docker:push unpacks every layer on the pushing machine. A layer
// that makes x -> /some/dir and a later layer with x/planted must not write
// into /some/dir on that machine; the file goes into the merged rootfs at
// some/dir/planted, where the container would see it.
func TestBuildFromSaveDirKeepsLayersInsideMergedRoot(t *testing.T) {
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saveDir := writeSaveDir(t, map[string]interface{}{"Entrypoint": []string{"/bin/app"}},
		layerTarEntry{name: "x", link: outside},
	)
	// a second layer that writes through the first layer's link
	f, err := os.Create(filepath.Join(saveDir, "layer2.tar"))
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	content := []byte("planted")
	if err := tw.WriteHeader(&tar.Header{Name: "x/planted", Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(content)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	data, _ := json.Marshal([]*Manifest{{Config: "config.json", Layers: []string{"layer.tar", "layer2.tar"}}})
	if err := os.WriteFile(filepath.Join(saveDir, "manifest.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	tc, _ := fakeTarreceive(t)
	if _, err := BuildFromSaveDir(tc, saveDir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("layer wrote %d entries into %s on the pushing machine", len(entries), outside)
	}
	got, err := os.ReadFile(filepath.Join(saveDir, "merged", outside, "planted"))
	if err != nil || string(got) != "planted" {
		t.Fatalf("merged rootfs %s/planted = %q, %v", outside, got, err)
	}
}
