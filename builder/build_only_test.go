package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func testManifestImages() []*Image {
	return []*Image{
		{ID: "ubuntu-noble"},
		{ID: "busybox"},
		{ID: "go", Layers: []*Layer{{BuildWith: "ubuntu-noble"}}},
		{ID: "protoc", Layers: []*Layer{{BuildWith: "ubuntu-noble"}}},
		{ID: "heroku-24"},
		{ID: "heroku-24-build"},
		{ID: "slugrunner-24"},
		{ID: "controller", Layers: []*Layer{
			{GoBuild: map[string]string{"controller": "/bin/controller"}},
			{ProtoBuild: []string{"controller/protos"}},
		}},
		{ID: "host", Layers: []*Layer{
			{GoBuild: map[string]string{"host": "/bin/flynn-host"}},
		}},
		{ID: "builder", Layers: []*Layer{
			{BuildWith: "go", Inputs: []string{"builder/**"}},
		}},
		{ID: "controller-examples"},
		{ID: "test", Layers: []*Layer{
			{CGoBuild: map[string]string{"test": "/bin/flynn-test"}},
		}},
		{ID: "test-apps", Layers: []*Layer{
			{GoBuild: map[string]string{"test/apps/echoer": "/bin/echoer"}},
		}},
	}
}

func imageIDs(images []*Image) []string {
	ids := make([]string, len(images))
	for i, img := range images {
		ids[i] = img.ID
	}
	return ids
}

func TestExpandImageSelectionToolchain(t *testing.T) {
	got, err := expandImageSelection(testManifestImages(), "toolchain")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ubuntu-noble", "busybox", "go", "protoc",
		"heroku-24", "heroku-24-build", "slugrunner-24",
	}
	if !reflect.DeepEqual(imageIDs(got), want) {
		t.Fatalf("toolchain ids = %v, want %v", imageIDs(got), want)
	}
}

func TestExpandImageSelectionAppsIncludesTransitiveDeps(t *testing.T) {
	got, err := expandImageSelection(testManifestImages(), "apps")
	if err != nil {
		t.Fatal(err)
	}
	ids := imageIDs(got)
	// apps themselves
	for _, id := range []string{"controller", "host", "builder"} {
		if !containsString(ids, id) {
			t.Fatalf("apps selection missing %q: %v", id, ids)
		}
	}
	// transitive go/protoc/ubuntu-noble deps (not other toolchain-only images)
	for _, id := range []string{"go", "protoc", "ubuntu-noble"} {
		if !containsString(ids, id) {
			t.Fatalf("apps selection missing transitive dep %q: %v", id, ids)
		}
	}
	// busybox/heroku/slugrunner are not deps of any app in the fixture
	for _, id := range []string{"busybox", "heroku-24", "heroku-24-build", "slugrunner-24"} {
		if containsString(ids, id) {
			t.Fatalf("apps selection unexpectedly includes %q: %v", id, ids)
		}
	}
	for _, id := range []string{"test", "test-apps", "controller-examples"} {
		if containsString(ids, id) {
			t.Fatalf("apps selection must omit test image %q: %v", id, ids)
		}
	}
}

func TestExpandImageSelectionTestGroup(t *testing.T) {
	got, err := expandImageSelection(testManifestImages(), "test")
	if err != nil {
		t.Fatal(err)
	}
	ids := imageIDs(got)
	for _, id := range []string{"test", "test-apps", "controller-examples"} {
		if !containsString(ids, id) {
			t.Fatalf("test selection missing %q: %v", id, ids)
		}
	}
	for _, id := range []string{"controller", "host", "heroku-24"} {
		if containsString(ids, id) {
			t.Fatalf("test selection unexpectedly includes %q: %v", id, ids)
		}
	}
}

func TestExpandImageSelectionTestMissingFromManifest(t *testing.T) {
	_, err := expandImageSelection([]*Image{{ID: "controller"}}, "test")
	if err == nil {
		t.Fatal("expected error when test images are missing from the manifest")
	}
}

func TestExpandImageSelectionExplicitIDsWithDeps(t *testing.T) {
	got, err := expandImageSelection(testManifestImages(), "controller,host")
	if err != nil {
		t.Fatal(err)
	}
	ids := imageIDs(got)
	sort.Strings(ids)
	want := []string{"controller", "go", "host", "protoc", "ubuntu-noble"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestExpandImageSelectionUnknown(t *testing.T) {
	_, err := expandImageSelection(testManifestImages(), "no-such-image")
	if err == nil {
		t.Fatal("expected error for unknown image")
	}
}

func TestExpandImageSelectionEmptyOnly(t *testing.T) {
	all := testManifestImages()
	got, err := expandImageSelection(all, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(all) {
		t.Fatalf("got %d images, want %d", len(got), len(all))
	}
}

func TestHasAllImageArtifactsIgnoresTestImages(t *testing.T) {
	images := []*Image{
		{ID: "controller"},
		{ID: "test"},
		{ID: "test-apps"},
		{ID: "controller-examples"},
	}
	b := &Builder{
		artifacts: map[string]*ct.Artifact{
			"controller": {URI: "file:///controller"},
		},
	}
	if !b.hasAllImageArtifacts(images) {
		t.Fatal("production artifacts should be enough without test images")
	}
	delete(b.artifacts, "controller")
	if b.hasAllImageArtifacts(images) {
		t.Fatal("missing production image must fail completeness check")
	}
}

func TestMergeAndWriteImagesJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "images.json")

	phase1 := map[string]*ct.Artifact{
		"go":     {URI: "file:///go", Type: ct.ArtifactTypeFlynn},
		"protoc": {URI: "file:///protoc", Type: ct.ArtifactTypeFlynn},
	}
	if err := writeArtifacts(path, phase1); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadArtifacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded %d artifacts, want 2", len(loaded))
	}

	phase2 := map[string]*ct.Artifact{
		"controller": {URI: "file:///controller", Type: ct.ArtifactTypeFlynn},
		"go":         {URI: "file:///go-updated", Type: ct.ArtifactTypeFlynn},
	}
	merged := mergeArtifacts(loaded, phase2)
	if err := writeArtifacts(path, merged); err != nil {
		t.Fatal(err)
	}

	final, err := loadArtifacts(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 3 {
		t.Fatalf("final count = %d, want 3", len(final))
	}
	if final["go"].URI != "file:///go-updated" {
		t.Fatalf("go URI = %q, want updated", final["go"].URI)
	}
	if final["protoc"].URI != "file:///protoc" {
		t.Fatalf("protoc should be preserved, got %q", final["protoc"].URI)
	}
	if final["controller"].URI != "file:///controller" {
		t.Fatalf("controller missing/wrong: %+v", final["controller"])
	}

	// ensure file is valid JSON object
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]*ct.Artifact
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestWriteImagesMergesExistingFile(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := os.MkdirAll("build", 0755); err != nil {
		t.Fatal(err)
	}
	existing := map[string]*ct.Artifact{
		"ubuntu-noble": {URI: "file:///ubuntu", Type: ct.ArtifactTypeFlynn},
		"go":           {URI: "file:///go", Type: ct.ArtifactTypeFlynn},
	}
	if err := writeArtifacts(imagesJSONPath, existing); err != nil {
		t.Fatal(err)
	}

	b := &Builder{
		artifacts: map[string]*ct.Artifact{
			"controller": {URI: "file:///controller", Type: ct.ArtifactTypeFlynn},
		},
	}
	if err := b.WriteImages(); err != nil {
		t.Fatal(err)
	}

	final, err := loadArtifacts(imagesJSONPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != 3 {
		t.Fatalf("merged count = %d, want 3", len(final))
	}
	for _, id := range []string{"ubuntu-noble", "go", "controller"} {
		if _, ok := final[id]; !ok {
			t.Fatalf("missing %q after WriteImages merge", id)
		}
	}
}

func TestLoadImageDirArtifacts(t *testing.T) {
	dir := t.TempDir()
	missing, err := loadImageDirArtifacts(filepath.Join(dir, "nope"))
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("missing dir should return nil, got %v", missing)
	}

	imageDir := filepath.Join(dir, "image")
	if err := os.MkdirAll(imageDir, 0755); err != nil {
		t.Fatal(err)
	}
	blobstore := &ct.Artifact{URI: "file:///blobstore", Type: ct.ArtifactTypeFlynn}
	raw, err := json.Marshal(blobstore)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "blobstore.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "notes.txt"), []byte("skip"), 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := loadImageDirArtifacts(imageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d artifacts, want 1", len(loaded))
	}
	if loaded["blobstore"].URI != "file:///blobstore" {
		t.Fatalf("blobstore URI = %q", loaded["blobstore"].URI)
	}

	existing := map[string]*ct.Artifact{
		"go": {URI: "file:///go", Type: ct.ArtifactTypeFlynn},
	}
	merged := mergeArtifacts(existing, loaded)
	if len(merged) != 2 {
		t.Fatalf("merged count = %d, want 2", len(merged))
	}
}

func TestWriteImagesAfterPartialBuild(t *testing.T) {
	dir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := os.MkdirAll("build", 0755); err != nil {
		t.Fatal(err)
	}
	b := &Builder{
		artifacts: map[string]*ct.Artifact{
			"controller": {URI: "file:///controller", Type: ct.ArtifactTypeFlynn},
		},
	}
	if err := b.WriteImages(); err != nil {
		t.Fatal(err)
	}
	final, err := loadArtifacts(imagesJSONPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := final["controller"]; !ok {
		t.Fatal("partial success must still persist images.json for retry")
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestNo386CLIImages(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	dir := filepath.Dir(thisFile)
	for _, name := range []string{"manifest.json", "manifest.json.template"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var mf Manifest
		if err := json.Unmarshal(raw, &mf); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, img := range mf.Images {
			if strings.Contains(img.ID, "386") {
				t.Errorf("%s still has 386 CLI image %q", name, img.ID)
			}
			if img.Env["GOARCH"] == "386" {
				t.Errorf("%s image %q still builds GOARCH=386", name, img.ID)
			}
		}
	}
}
