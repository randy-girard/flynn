package dockerimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/archive"
	hh "github.com/flynn/flynn/pkg/httphelper"
	tarclient "github.com/flynn/flynn/tarreceive/client"
)

// BuildResult holds the image manifest and process metadata produced from a
// docker save directory.
type BuildResult struct {
	Manifest   *ct.ImageManifest
	Config     *Config
	Args       []string
	ListenPort int
}

// BuildFromSaveDir merges docker save layers and uploads them to tarreceive.
func BuildFromSaveDir(tarClient *tarclient.Client, saveDir string) (*BuildResult, error) {
	manifest, err := loadManifest(saveDir)
	if err != nil {
		return nil, err
	}

	config, err := loadConfig(saveDir, manifest.Config)
	if err != nil {
		return nil, err
	}

	mergedDir := filepath.Join(saveDir, "merged")
	if err := os.MkdirAll(mergedDir, 0755); err != nil {
		return nil, fmt.Errorf("error creating merged rootfs directory: %s", err)
	}
	for _, path := range manifest.Layers {
		f, err := openLayer(filepath.Join(saveDir, path))
		if err != nil {
			return nil, fmt.Errorf("error opening docker layer %s: %s", path, err)
		}
		if err := archive.UnpackFlat(f, mergedDir, false); err != nil {
			f.Close()
			return nil, fmt.Errorf("error applying docker layer %s: %s", path, err)
		}
		f.Close()
	}

	mergedTarPath := filepath.Join(saveDir, "merged.tar")
	cmd := exec.Command("tar", "-cf", mergedTarPath, "-C", mergedDir, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("error creating merged layer tar: %s", err)
	}

	mergedTar, err := os.Open(mergedTarPath)
	if err != nil {
		return nil, err
	}
	defer mergedTar.Close()

	tarHash := sha256.New()
	if _, err := io.Copy(tarHash, mergedTar); err != nil {
		return nil, fmt.Errorf("error hashing merged layer tar: %s", err)
	}
	layerID := hex.EncodeToString(tarHash.Sum(nil))
	if _, err := mergedTar.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("error seeking merged layer tar: %s", err)
	}

	layer, err := tarClient.GetLayer(layerID)
	if err == tarclient.ErrNotFound {
		layer, err = tarClient.CreateFlatLayer(layerID, mergedTar)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	args := ResolveArgs(mergedDir, config.Config.Entrypoint, config.Config.Cmd)
	if len(args) > 0 && !strings.Contains(args[0], "/") {
		if findExecutableInRoot(mergedDir, args[0]) == "" {
			return nil, fmt.Errorf("image entrypoint %q not found in merged rootfs; verify the Docker image includes its runtime", args[0])
		}
	}

	entrypoint := &ct.ImageEntrypoint{
		WorkingDir: config.Config.WorkingDir,
		Env:        make(map[string]string, len(config.Config.Env)),
		Args:       args,
	}
	for _, env := range config.Config.Env {
		keyVal := strings.SplitN(env, "=", 2)
		if len(keyVal) != 2 {
			continue
		}
		val := strings.Replace(keyVal[1], "\t", "\\t", -1)
		entrypoint.Env[keyVal[0]] = val
	}
	image := &ct.ImageManifest{
		Type:        ct.ImageManifestTypeV1,
		Entrypoints: map[string]*ct.ImageEntrypoint{"_default": entrypoint},
		Rootfs: []*ct.ImageRootfs{{
			Platform: ct.DefaultImagePlatform,
			Layers:   []*ct.ImageLayer{layer},
		}},
	}

	return &BuildResult{
		Manifest:   image,
		Config:     config,
		Args:       args,
		ListenPort: ListenPort(config.Config.ExposedPorts),
	}, nil
}

// CreateArtifact uploads the image manifest to the blobstore and registers the
// artifact with the controller.
func CreateArtifact(client interface {
	CreateArtifact(*ct.Artifact) error
}, build *BuildResult, id string, meta map[string]string) (*ct.Artifact, error) {
	rawManifest := build.Manifest.RawManifest()
	imageURL := fmt.Sprintf("http://blobstore.discoverd/tarreceive/images/%s.json", build.Manifest.ID())
	if err := uploadManifest(rawManifest, imageURL); err != nil {
		return nil, err
	}
	artifactMeta := map[string]string{"blobstore": "true"}
	for k, v := range meta {
		artifactMeta[k] = v
	}
	artifact := &ct.Artifact{
		ID:               id,
		Type:             ct.ArtifactTypeFlynn,
		URI:              imageURL,
		Meta:             artifactMeta,
		RawManifest:      rawManifest,
		Hashes:           build.Manifest.Hashes(),
		Size:             int64(len(rawManifest)),
		LayerURLTemplate: "http://blobstore.discoverd/tarreceive/layers/{id}.squashfs",
	}
	if id == "" {
		artifact.ID = ""
	}
	if err := client.CreateArtifact(artifact); err != nil {
		return nil, err
	}
	return artifact, nil
}

func uploadManifest(data []byte, url string) error {
	req, err := http.NewRequest("PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	res, err := hh.RetryClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	return nil
}

// PushFromSaveDir merges docker save layers, uploads to tarreceive, and returns
// the created artifact along with resolved process configuration.
func PushFromSaveDir(tarClient *tarclient.Client, saveDir string) (*ct.Artifact, *BuildResult, error) {
	result, err := ImportSaveDir(saveDir, ImportOptions{TarClient: tarClient})
	if err != nil {
		return nil, nil, err
	}
	return result.Artifact, result.Build, nil
}

func loadManifest(saveDir string) (*Manifest, error) {
	f, err := os.Open(filepath.Join(saveDir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var manifests []*Manifest
	if err := json.NewDecoder(f).Decode(&manifests); err != nil {
		return nil, err
	}
	if len(manifests) != 1 {
		return nil, fmt.Errorf("expected 1 docker manifest, got %d", len(manifests))
	}
	return manifests[0], nil
}

func loadConfig(saveDir, configPath string) (*Config, error) {
	f, err := os.Open(filepath.Join(saveDir, configPath))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var config Config
	return &config, json.NewDecoder(f).Decode(&config)
}

// UnpackSave extracts a docker save tarball into dir.
func UnpackSave(r io.Reader, dir string) error {
	return archive.Unpack(r, dir, false)
}
