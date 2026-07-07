package dockerimage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestListenPort(t *testing.T) {
	tests := []struct {
		name    string
		exposed map[string]interface{}
		want    int
	}{
		{"empty", nil, 8080},
		{"prefer 80", map[string]interface{}{"80/tcp": struct{}{}}, 80},
		{"prefer 8080", map[string]interface{}{"3000/tcp": struct{}{}, "8080/tcp": struct{}{}}, 8080},
		{"lowest", map[string]interface{}{"9000/tcp": struct{}{}, "3000/tcp": struct{}{}}, 3000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ListenPort(tc.exposed); got != tc.want {
				t.Fatalf("ListenPort() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolveArgs(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(binDir, "myapp"), []byte{0}, 0755); err != nil {
		t.Fatal(err)
	}

	args := ResolveArgs(root, []string{"myapp"}, []string{"start"})
	if len(args) != 2 || args[0] != "/usr/local/bin/myapp" || args[1] != "start" {
		t.Fatalf("ResolveArgs() = %#v", args)
	}
}

func TestBuildResultFromInspect(t *testing.T) {
	build := BuildResultFromInspect(
		[]string{"/entry"},
		[]string{"cmd"},
		[]string{"FOO=bar"},
		map[string]interface{}{"8080/tcp": struct{}{}},
	)
	if len(build.Args) != 2 || build.Args[0] != "/entry" || build.Args[1] != "cmd" {
		t.Fatalf("Args = %#v", build.Args)
	}
	if build.ListenPort != 8080 {
		t.Fatalf("ListenPort = %d", build.ListenPort)
	}
}

func TestNewAppRelease(t *testing.T) {
	prev := &ct.Release{
		Env: map[string]string{"KEEP": "yes"},
	}
	build := &BuildResult{
		Args:       []string{"/bin/sh", "-c", "app"},
		ListenPort: 3000,
		Config: &Config{},
	}
	build.Config.Config.Env = []string{"IMAGE_ENV=1", "KEEP=overwrite"}

	release := NewAppRelease("myapp", prev, "artifact-id", build, ReleaseOptions{
		Env: map[string]string{"KEEP": "yes"},
		Meta: map[string]string{
			"git": "true",
		},
		ServiceHealthCheck: true,
	})

	if len(release.ArtifactIDs) != 1 || release.ArtifactIDs[0] != "artifact-id" {
		t.Fatalf("ArtifactIDs = %#v", release.ArtifactIDs)
	}
	proc := release.Processes["app"]
	if proc.Args[0] != "/bin/sh" {
		t.Fatalf("Args = %#v", proc.Args)
	}
	if proc.Ports[0].Port != 3000 {
		t.Fatalf("Port = %d", proc.Ports[0].Port)
	}
	if proc.Ports[0].Service.Check == nil {
		t.Fatal("expected health check")
	}
	if release.Env["KEEP"] != "yes" {
		t.Fatalf("Env KEEP = %q", release.Env["KEEP"])
	}
	if release.Env["IMAGE_ENV"] != "1" {
		t.Fatalf("Env IMAGE_ENV = %q", release.Env["IMAGE_ENV"])
	}
	if release.Meta["git"] != "true" {
		t.Fatalf("Meta = %#v", release.Meta)
	}
}

func TestNewAppReleaseFromArtifact(t *testing.T) {
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Entrypoints: map[string]*ct.ImageEntrypoint{
			"_default": {
				Args: []string{"/docker-entrypoint.sh"},
				Env:  map[string]string{"PORT": "8080"},
			},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifact := &ct.Artifact{
		ID:          "image-1",
		RawManifest: raw,
		Meta: map[string]string{
			MetaListenPort: "4000",
		},
	}

	release := NewAppReleaseFromArtifact("shop", nil, artifact, ReleaseOptions{})
	proc := release.Processes["app"]
	if proc.Args[0] != "/docker-entrypoint.sh" {
		t.Fatalf("Args = %#v", proc.Args)
	}
	if proc.Ports[0].Port != 4000 {
		t.Fatalf("Port = %d", proc.Ports[0].Port)
	}
}

func TestBuildFromSaveDir(t *testing.T) {
	saveDir := writeDockerSaveFixture(t)

	manifest, err := loadManifest(saveDir)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Config == "" || len(manifest.Layers) != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	config, err := loadConfig(saveDir, manifest.Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != "/hello" {
		t.Fatalf("config = %#v", config.Config)
	}
}

func writeDockerSaveFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	layerName := "layer.tar"
	layerPath := filepath.Join(dir, layerName)
	if err := writeLayerTar(layerPath, "hello\n"); err != nil {
		t.Fatal(err)
	}

	config := map[string]interface{}{
		"config": map[string]interface{}{
			"Entrypoint":   []string{"/hello"},
			"ExposedPorts": map[string]interface{}{"8080/tcp": struct{}{}},
		},
	}
	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configName := "config.json"
	if err := ioutil.WriteFile(filepath.Join(dir, configName), configData, 0644); err != nil {
		t.Fatal(err)
	}

	manifest := []*Manifest{{
		Config: configName,
		Layers: []string{layerName},
	}}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(dir, "manifest.json"), manifestData, 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeLayerTar(path, content string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "hello.txt",
		Mode: 0644,
		Size: int64(len(content)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func TestUnpackSave(t *testing.T) {
	saveDir := writeDockerSaveFixture(t)
	var buf bytes.Buffer
	if err := packDir(&buf, saveDir); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := UnpackSave(&buf, outDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func packDir(w *bytes.Buffer, dir string) error {
	tw := tar.NewWriter(w)
	defer tw.Close()
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: rel,
			Mode: int64(info.Mode()),
			Size: int64(len(data)),
		}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
}
