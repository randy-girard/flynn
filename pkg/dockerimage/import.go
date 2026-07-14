package dockerimage

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"

	ct "github.com/flynn/flynn/controller/types"
	tarclient "github.com/flynn/flynn/tarreceive/client"
)

// ImportOptions configures importing a docker save archive into Flynn.
type ImportOptions struct {
	TarClient        *tarclient.Client
	ControllerClient interface {
		CreateArtifact(*ct.Artifact) error
	}
	ArtifactID   string
	ArtifactMeta map[string]string
}

// ImportResult is the outcome of importing a docker save archive.
type ImportResult struct {
	Artifact *ct.Artifact
	Build    *BuildResult
}

// ImportSaveReader unpacks a docker save tarball and imports it through tarreceive.
func ImportSaveReader(r io.Reader, opts ImportOptions) (*ImportResult, error) {
	tmpDir, err := ioutil.TempDir("", "flynn-docker-save")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	if err := UnpackSave(r, tmpDir); err != nil {
		return nil, fmt.Errorf("error extracting docker save output: %s", err)
	}
	return ImportSaveDir(tmpDir, opts)
}

// ImportSaveFile imports a docker save tarball from path.
func ImportSaveFile(path string, opts ImportOptions) (*ImportResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ImportSaveReader(f, opts)
}

// ImportSaveDir imports an unpacked docker save directory.
func ImportSaveDir(saveDir string, opts ImportOptions) (*ImportResult, error) {
	if opts.TarClient == nil {
		return nil, fmt.Errorf("missing tarreceive client")
	}
	build, err := BuildFromSaveDir(opts.TarClient, saveDir)
	if err != nil {
		return nil, err
	}
	meta := ArtifactMeta(build)
	for k, v := range opts.ArtifactMeta {
		meta[k] = v
	}
	var artifact *ct.Artifact
	if opts.ControllerClient != nil {
		if opts.ArtifactID == "" {
			return nil, fmt.Errorf("missing artifact ID")
		}
		artifact, err = CreateArtifact(opts.ControllerClient, build, opts.ArtifactID, meta)
	} else {
		artifact, err = opts.TarClient.CreateArtifact(build.Manifest)
		if err == nil && len(meta) > 0 {
			artifact.Meta = meta
		}
	}
	if err != nil {
		return nil, err
	}
	return &ImportResult{Artifact: artifact, Build: build}, nil
}
