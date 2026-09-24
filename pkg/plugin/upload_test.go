package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestArtifactLayerURLReusesMetaOverride(t *testing.T) {
	osLayer := &ct.ImageLayer{ID: "os-layer"}
	delta := &ct.ImageLayer{ID: "delta-layer"}
	art := &ct.Artifact{
		LayerURLTemplate: "http://blobstore.discoverd/plugins/widget/layers/{id}.squashfs",
		Meta: map[string]string{
			"layer_url.os-layer": "http://blobstore.discoverd/layers/os-layer.squashfs",
		},
	}
	if got := art.LayerURL(osLayer); got != "http://blobstore.discoverd/layers/os-layer.squashfs" {
		t.Fatalf("OS layer URL=%s", got)
	}
	if got := art.LayerURL(delta); got != "http://blobstore.discoverd/plugins/widget/layers/delta-layer.squashfs" {
		t.Fatalf("delta layer URL=%s", got)
	}
}

func TestBlobstoreHasObjectHeadAndGetFallback(t *testing.T) {
	objects := map[string][]byte{"/layers/a.squashfs": []byte("layer-bytes")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := objects[r.URL.Path]
		switch r.Method {
		case http.MethodHead:
			if body == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if body == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	url := srv.URL + "/layers/a.squashfs"
	if !blobstoreHasObject(srv.Client(), url, int64(len("layer-bytes"))) {
		t.Fatal("HEAD 200 matching size must skip")
	}
	if blobstoreHasObject(srv.Client(), url, 99) {
		t.Fatal("size mismatch must not skip")
	}
	if blobstoreHasObject(srv.Client(), srv.URL+"/missing", 1) {
		t.Fatal("404 must not skip")
	}

	getOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body := []byte("layer-bytes")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(body)))
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[:1])
			return
		}
		w.Write(body)
	}))
	defer getOnly.Close()
	if !blobstoreHasObject(getOnly.Client(), getOnly.URL+"/layers/a.squashfs", int64(len("layer-bytes"))) {
		t.Fatal("GET fallback after HEAD 405 must skip")
	}
}

func TestUploadDistLayersSkipsExistingPluginPrefix(t *testing.T) {
	osBody := bytes.Repeat([]byte("O"), 32)
	deltaBody := bytes.Repeat([]byte("D"), 16)
	dist := testUploadDist(t, osBody, deltaBody)
	store := newFakeBlobstore()
	srv := httptest.NewServer(store)
	defer srv.Close()

	var log bytes.Buffer
	in := &Installer{
		HTTP:            srv.Client(),
		Stdout:          &log,
		BlobstorePrefix: srv.URL + "/plugins",
		LayerCacheDir:   t.TempDir(),
	}
	t.Setenv(EnvImagesJSON, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(EnvLayersDir, t.TempDir())

	if _, meta, err := in.uploadDistLayers("widget", dist); err != nil {
		t.Fatal(err)
	} else if len(meta) != 0 {
		t.Fatalf("first upload meta=%v", meta)
	}
	if store.putCount() != 2 {
		t.Fatalf("first upload PUTs=%d", store.putCount())
	}

	log.Reset()
	store.resetPuts()
	_, meta, err := in.uploadDistLayers("widget", dist)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 0 {
		t.Fatalf("skip upload must keep plugin-prefix URLs, meta=%v", meta)
	}
	if store.putCount() != 0 {
		t.Fatalf("second upload PUTs=%d want 0", store.putCount())
	}
	if !strings.Contains(log.String(), "already in blobstore") {
		t.Fatalf("log=%q", log.String())
	}
}

func TestUploadDistLayersSizeMismatchReuploads(t *testing.T) {
	osBody := bytes.Repeat([]byte("O"), 32)
	deltaBody := bytes.Repeat([]byte("D"), 16)
	dist := testUploadDist(t, osBody, deltaBody)
	store := newFakeBlobstore()
	srv := httptest.NewServer(store)
	defer srv.Close()

	in := &Installer{
		HTTP:            srv.Client(),
		Stdout:          io.Discard,
		BlobstorePrefix: srv.URL + "/plugins",
		LayerCacheDir:   t.TempDir(),
	}
	t.Setenv(EnvImagesJSON, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(EnvLayersDir, t.TempDir())

	store.set("/plugins/widget/layers/os-layer.squashfs", []byte("wrong-size"))
	store.set("/plugins/widget/layers/delta-layer.squashfs", deltaBody)

	if _, _, err := in.uploadDistLayers("widget", dist); err != nil {
		t.Fatal(err)
	}
	puts := store.putPaths()
	if len(puts) != 1 || puts[0] != "/plugins/widget/layers/os-layer.squashfs" {
		t.Fatalf("size mismatch must re-PUT OS layer, puts=%v", puts)
	}
}

func TestUploadDistLayersLocalFlynnCacheDoesNotSkipWithoutBlobstore(t *testing.T) {
	osBody := bytes.Repeat([]byte("O"), 32)
	deltaBody := bytes.Repeat([]byte("D"), 16)
	dist := testUploadDist(t, osBody, deltaBody)
	cache := t.TempDir()
	if err := os.WriteFile(filepath.Join(cache, "os-layer.squashfs"), osBody, 0644); err != nil {
		t.Fatal(err)
	}
	store := newFakeBlobstore()
	srv := httptest.NewServer(store)
	defer srv.Close()

	in := &Installer{
		HTTP:            srv.Client(),
		Stdout:          io.Discard,
		BlobstorePrefix: srv.URL + "/plugins",
		LayerCacheDir:   cache,
	}
	t.Setenv(EnvImagesJSON, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv(EnvLayersDir, cache)

	if _, meta, err := in.uploadDistLayers("widget", dist); err != nil {
		t.Fatal(err)
	} else if len(meta) != 0 {
		t.Fatalf("local cache without fetchable URL must PUT, meta=%v", meta)
	}
	if store.putCount() != 2 {
		t.Fatalf("local Flynn cache must not skip plugin-prefix PUT, PUTs=%d", store.putCount())
	}
}

func TestUploadDistLayersReusesFlynnLayerURL(t *testing.T) {
	osBody := bytes.Repeat([]byte("O"), 32)
	deltaBody := bytes.Repeat([]byte("D"), 16)
	dist := testUploadDist(t, osBody, deltaBody)
	store := newFakeBlobstore()
	srv := httptest.NewServer(store)
	defer srv.Close()
	store.set("/layers/os-layer.squashfs", osBody)

	img := filepath.Join(t.TempDir(), "images.json")
	writeFlynnImagesJSON(t, img, srv.URL+"/layers/{id}.squashfs", "os-layer", int64(len(osBody)))
	t.Setenv(EnvImagesJSON, img)
	t.Setenv(EnvLayersDir, t.TempDir())

	var log bytes.Buffer
	in := &Installer{
		HTTP:            srv.Client(),
		Stdout:          &log,
		BlobstorePrefix: srv.URL + "/plugins",
		LayerCacheDir:   t.TempDir(),
	}
	tmpl, meta, err := in.uploadDistLayers("widget", dist)
	if err != nil {
		t.Fatal(err)
	}
	wantFlynn := srv.URL + "/layers/os-layer.squashfs"
	if meta["layer_url.os-layer"] != wantFlynn {
		t.Fatalf("meta=%v", meta)
	}
	if store.putCount() != 1 || store.putPaths()[0] != "/plugins/widget/layers/delta-layer.squashfs" {
		t.Fatalf("must PUT overlay only, puts=%v", store.putPaths())
	}
	if strings.Contains(strings.Join(store.putPaths(), " "), "os-layer") {
		t.Fatal("must not PUT Flynn OS layer into plugin prefix")
	}
	if !strings.Contains(log.String(), "already in blobstore") {
		t.Fatalf("log=%q", log.String())
	}
	art := &ct.Artifact{LayerURLTemplate: tmpl, Meta: meta}
	if got := art.LayerURL(&ct.ImageLayer{ID: "os-layer"}); got != wantFlynn {
		t.Fatalf("job OS URL=%s", got)
	}
	wantDelta := srv.URL + "/plugins/widget/layers/delta-layer.squashfs"
	if got := art.LayerURL(&ct.ImageLayer{ID: "delta-layer"}); got != wantDelta {
		t.Fatalf("job delta URL=%s", got)
	}
}

func TestUploadDistLayersOverlayNotSkippedByFlynnCache(t *testing.T) {
	osBody := bytes.Repeat([]byte("O"), 32)
	deltaBody := bytes.Repeat([]byte("D"), 16)
	dist := testUploadDist(t, osBody, deltaBody)
	store := newFakeBlobstore()
	srv := httptest.NewServer(store)
	defer srv.Close()
	store.set("/layers/os-layer.squashfs", osBody)
	store.set("/layers/delta-layer.squashfs", deltaBody)

	img := filepath.Join(t.TempDir(), "images.json")
	writeFlynnImagesJSON(t, img, srv.URL+"/layers/{id}.squashfs", "os-layer", int64(len(osBody)))
	t.Setenv(EnvImagesJSON, img)
	t.Setenv(EnvLayersDir, t.TempDir())

	in := &Installer{
		HTTP:            srv.Client(),
		Stdout:          io.Discard,
		BlobstorePrefix: srv.URL + "/plugins",
		LayerCacheDir:   t.TempDir(),
	}
	if _, _, err := in.uploadDistLayers("widget", dist); err != nil {
		t.Fatal(err)
	}
	if store.putCount() != 1 || store.putPaths()[0] != "/plugins/widget/layers/delta-layer.squashfs" {
		t.Fatalf("overlay must still upload, puts=%v", store.putPaths())
	}
}

func TestLayerURLFromArtifactsPrefersNonPlugin(t *testing.T) {
	osID := "os-layer"
	pluginArt := &ct.Artifact{
		LayerURLTemplate: "http://blobstore.discoverd/plugins/other/layers/{id}.squashfs",
		Meta:             map[string]string{"flynn.plugin": "true"},
		RawManifest: stackedManifest([]*ct.ImageLayer{{
			ID:     osID,
			Type:   ct.ImageLayerTypeSquashfs,
			Length: 32,
		}}),
	}
	flynnArt := &ct.Artifact{
		LayerURLTemplate: "http://blobstore.discoverd/layers/{id}.squashfs",
		Meta:             map[string]string{"flynn.system-image": "true"},
		RawManifest: stackedManifest([]*ct.ImageLayer{{
			ID:     osID,
			Type:   ct.ImageLayerTypeSquashfs,
			Length: 32,
		}}),
	}
	got := layerURLFromArtifacts([]*ct.Artifact{pluginArt, flynnArt}, osID)
	if got != "http://blobstore.discoverd/layers/os-layer.squashfs" {
		t.Fatalf("got %s", got)
	}
	if got := layerURLFromArtifacts([]*ct.Artifact{pluginArt}, osID); got != "http://blobstore.discoverd/plugins/other/layers/os-layer.squashfs" {
		t.Fatalf("plugin fallback=%s", got)
	}
}

func TestFlynnLayerURLFromImagesJSON(t *testing.T) {
	id := "os-layer"
	img := filepath.Join(t.TempDir(), "images.json")
	writeFlynnImagesJSON(t, img, "http://blobstore.discoverd/layers/{id}.squashfs", id, 32)
	t.Setenv(EnvImagesJSON, img)
	in := &Installer{}
	if got := in.flynnLayerURL(id); got != "http://blobstore.discoverd/layers/os-layer.squashfs" {
		t.Fatalf("got %s", got)
	}
	if in.flynnLayerURL("missing") != "" {
		t.Fatal("unknown layer")
	}
}

type fakeBlobstore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []string
}

func newFakeBlobstore() *fakeBlobstore {
	return &fakeBlobstore{objects: map[string][]byte{}}
}

func (f *fakeBlobstore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodHead:
		body, ok := f.objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		body, ok := f.objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Header.Get("Range") != "" && len(body) > 0 {
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(body[:1])
			return
		}
		w.Write(body)
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[r.URL.Path] = body
		f.puts = append(f.puts, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeBlobstore) set(path string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[path] = append([]byte(nil), body...)
}

func (f *fakeBlobstore) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

func (f *fakeBlobstore) putPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.puts))
	copy(out, f.puts)
	return out
}

func (f *fakeBlobstore) resetPuts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = nil
}

func testUploadDist(t *testing.T, osBody, deltaBody []byte) *DistArtifact {
	t.Helper()
	root := t.TempDir()
	ls := []*ct.ImageLayer{
		{
			ID:     "os-layer",
			Type:   ct.ImageLayerTypeSquashfs,
			Length: int64(len(osBody)),
			Hashes: map[string]string{"sha512_256": "os-layer"},
		},
		{
			ID:     "delta-layer",
			Type:   ct.ImageLayerTypeSquashfs,
			Length: int64(len(deltaBody)),
			Hashes: map[string]string{"sha512_256": "delta-layer"},
		},
	}
	distDir := filepath.Join(root, DistDir, "layers")
	if err := os.MkdirAll(distDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "os-layer.squashfs"), osBody, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "delta-layer.squashfs"), deltaBody, 0644); err != nil {
		t.Fatal(err)
	}
	raw := stackedManifest(ls)
	return &DistArtifact{
		Root: root,
		Dist: filepath.Join(root, DistDir),
		Artifact: &ct.Artifact{
			Type:        ct.ArtifactTypeFlynn,
			RawManifest: raw,
			Hashes:      map[string]string{"sha512_256": "manifest"},
			Size:        int64(len(raw)),
		},
	}
}

func stackedManifest(ls []*ct.ImageLayer) json.RawMessage {
	m := &ct.ImageManifest{
		Type:   ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{Layers: ls}},
	}
	return m.RawManifest()
}

func writeFlynnImagesJSON(t *testing.T, path, layerTmpl, layerID string, size int64) {
	t.Helper()
	art := &ct.Artifact{
		Type:             ct.ArtifactTypeFlynn,
		LayerURLTemplate: layerTmpl,
		Meta:             map[string]string{"flynn.system-image": "true"},
		RawManifest: stackedManifest([]*ct.ImageLayer{{
			ID:     layerID,
			Type:   ct.ImageLayerTypeSquashfs,
			Length: size,
			Hashes: map[string]string{"sha512_256": layerID},
		}}),
	}
	writeJSON(t, path, map[string]*ct.Artifact{"ubuntu-noble": art})
}
