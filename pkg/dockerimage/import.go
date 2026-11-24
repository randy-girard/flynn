package dockerimage

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"

	ct "github.com/randy-girard/flynn/controller/types"
	tarclient "github.com/randy-girard/flynn/tarreceive/client"
)

// ImportOptions configures importing a docker save archive into Flynn.
type ImportOptions struct {
	TarClient        *tarclient.Client
	ControllerClient interface {
		CreateArtifact(*ct.Artifact) error
	}
	ArtifactID   string
	ArtifactMeta map[string]string
	// AppID puts the image manifest and its layers under that app's
	// blobstore prefix (a build token reaches no other), where the layers
	// count toward the app's APP_QUOTA. Empty keeps the legacy shared path
	// unless TarClient names an app itself.
	AppID string
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
	build, err := BuildFromSaveDir(opts.tarClient(), saveDir)
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
		// the prefix the layers went to (tarClient names opts.AppID
		// when it is set)
		artifact, err = CreateArtifact(opts.ControllerClient, build, opts.ArtifactID, meta, opts.tarClient().App)
	} else {
		artifact, err = opts.tarClient().CreateArtifact(build.Manifest)
		if err == nil && len(meta) > 0 {
			artifact.Meta = meta
		}
	}
	if err != nil {
		return nil, err
	}
	return &ImportResult{Artifact: artifact, Build: build}, nil
}

// tarClient is the tarreceive client for the import: with an AppID, a copy
// that names the app, so the layers go under the same prefix as the
// manifest and the artifact's layer template (EDGE-35).
func (o ImportOptions) tarClient() *tarclient.Client {
	if o.AppID == "" || o.TarClient == nil || o.TarClient.App == o.AppID {
		return o.TarClient
	}
	c := *o.TarClient
	c.App = o.AppID
	return &c
}
