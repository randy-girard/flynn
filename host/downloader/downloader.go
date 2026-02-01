package downloader

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/volume"
	volumemanager "github.com/flynn/flynn/host/volume/manager"
	"github.com/flynn/flynn/pkg/ghrelease"
	"github.com/inconshreveable/log15"
)

const (
	maxDownloadRetries = 3
	retryDelay         = 2 * time.Second
)

var binaries = []string{
	"flynn-host",
	"flynn-linux-amd64",
	"flynn-init",
}

var config = []string{
	"bootstrap-manifest.json",
}

// Downloader downloads versioned files from GitHub releases
type Downloader struct {
	client  *ghrelease.Client
	repo    string
	vman    *volumemanager.Manager
	version string
	log     log15.Logger
}

// New creates a new Downloader that uses GitHub releases
func New(repo string, vman *volumemanager.Manager, version string, log log15.Logger) *Downloader {
	return &Downloader{
		client:  ghrelease.NewClient(repo, log),
		repo:    repo,
		vman:    vman,
		version: version,
		log:     log,
	}
}

// DownloadBinaries downloads the Flynn binaries from GitHub releases to the
// given dir with the version suffixed (e.g. /usr/local/bin/flynn-host.v20150726.0)
// and updates non-versioned symlinks.
func (d *Downloader) DownloadBinaries(dir string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("error creating bin dir: %s", err)
	}
	paths := make(map[string]string, len(binaries))
	for _, bin := range binaries {
		path, err := d.downloadGzippedFile(bin, dir, true)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0755); err != nil {
			return nil, err
		}
		paths[bin] = path
	}
	// symlink flynn to flynn-linux-amd64
	if err := symlink("flynn-linux-amd64", filepath.Join(dir, "flynn")); err != nil {
		return nil, err
	}
	return paths, nil
}

// DownloadConfig downloads the Flynn config files from GitHub releases to the
// given dir.
func (d *Downloader) DownloadConfig(dir string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("error creating config dir: %s", err)
	}
	paths := make(map[string]string, len(config))
	for _, conf := range config {
		path, err := d.downloadGzippedFile(conf, dir, false)
		if err != nil {
			return nil, err
		}
		paths[conf] = path
	}
	return paths, nil
}

// downloadWithRetry wraps the download with retry logic
func (d *Downloader) downloadWithRetry(assetURL, destPath string) error {
	var lastErr error
	for attempt := 1; attempt <= maxDownloadRetries; attempt++ {
		err := d.client.DownloadFile(assetURL, destPath)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < maxDownloadRetries {
			d.log.Warn("download failed, retrying", "attempt", attempt, "err", err)
			time.Sleep(retryDelay)
		}
	}
	return fmt.Errorf("download failed after %d attempts: %s", maxDownloadRetries, lastErr)
}

// downloadGzippedFile downloads a gzipped file from GitHub releases, decompresses it,
// and optionally creates a versioned file with a symlink.
func (d *Downloader) downloadGzippedFile(name, dir string, versioned bool) (string, error) {
	// Construct the asset URL
	assetName := name + ".gz"
	assetURL := ghrelease.GetReleaseURL(d.repo, d.version) + "/" + assetName

	// Download to temp file
	tmpPath := filepath.Join(dir, assetName+".tmp")
	if err := d.downloadWithRetry(assetURL, tmpPath); err != nil {
		return "", fmt.Errorf("error downloading %s: %s", name, err)
	}
	defer os.Remove(tmpPath)

	// Open and decompress
	gzFile, err := os.Open(tmpPath)
	if err != nil {
		return "", err
	}
	defer gzFile.Close()

	gz, err := gzip.NewReader(gzFile)
	if err != nil {
		return "", fmt.Errorf("error creating gzip reader for %s: %s", name, err)
	}
	defer gz.Close()

	// Determine destination path
	var destPath string
	if versioned {
		destPath = filepath.Join(dir, name+"."+d.version)
	} else {
		destPath = filepath.Join(dir, name)
	}

	// Write decompressed content
	destFile, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(destFile, gz); err != nil {
		destFile.Close()
		os.Remove(destPath)
		return "", fmt.Errorf("error decompressing %s: %s", name, err)
	}
	destFile.Close()

	// Create symlink for versioned files
	if versioned {
		if err := symlink(filepath.Base(destPath), filepath.Join(dir, name)); err != nil {
			return "", err
		}
	}

	return destPath, nil
}

// symlink creates a symlink, removing any existing file/symlink first
func symlink(target, link string) error {
	os.Remove(link)
	return os.Symlink(target, link)
}

// DownloadImages downloads container images from GitHub releases.
// It downloads the images manifest and then downloads each layer.
func (d *Downloader) DownloadImages(configDir string, ch chan *ct.ImagePullInfo) error {
	defer close(ch)

	// Download images manifest
	manifestURL := ghrelease.GetReleaseURL(d.repo, d.version) + "/images.json.gz"
	manifestPath := filepath.Join(configDir, "images.json.gz.tmp")
	if err := d.downloadWithRetry(manifestURL, manifestPath); err != nil {
		return fmt.Errorf("error downloading images manifest: %s", err)
	}
	defer os.Remove(manifestPath)

	// Decompress manifest
	gzFile, err := os.Open(manifestPath)
	if err != nil {
		return err
	}
	defer gzFile.Close()

	gz, err := gzip.NewReader(gzFile)
	if err != nil {
		return fmt.Errorf("error creating gzip reader for images manifest: %s", err)
	}
	defer gz.Close()

	// Parse manifest
	var images map[string]*ct.Artifact
	if err := json.NewDecoder(gz).Decode(&images); err != nil {
		return fmt.Errorf("error parsing images manifest: %s", err)
	}

	// Download each image's layers
	layerCacheDir := "/var/lib/flynn/layer-cache"
	if err := os.MkdirAll(layerCacheDir, 0755); err != nil {
		return fmt.Errorf("error creating layer cache dir: %s", err)
	}

	for name, artifact := range images {
		ch <- &ct.ImagePullInfo{
			Type:     ct.ImagePullTypeImage,
			Name:     name,
			Artifact: artifact,
		}

		manifest := artifact.Manifest()
		if manifest == nil {
			continue
		}

		for _, rootfs := range manifest.Rootfs {
			for _, layer := range rootfs.Layers {
				// Check if layer already exists
				layerPath := filepath.Join(layerCacheDir, layer.ID+".squashfs")
				if _, err := os.Stat(layerPath); err == nil {
					continue // Layer already cached
				}

				ch <- &ct.ImagePullInfo{
					Type:  ct.ImagePullTypeLayer,
					Name:  name,
					Layer: layer,
				}

				// Download layer
				if err := d.downloadLayer(layer, layerCacheDir); err != nil {
					return fmt.Errorf("error downloading layer %s: %s", layer.ID, err)
				}

				// Import layer into volume manager
				if d.vman != nil {
					if err := d.importLayer(layer, layerPath); err != nil {
						return fmt.Errorf("error importing layer %s: %s", layer.ID, err)
					}
				}
			}
		}
	}

	return nil
}

// downloadLayer downloads a single layer from GitHub releases
func (d *Downloader) downloadLayer(layer *ct.ImageLayer, cacheDir string) error {
	layerURL := ghrelease.GetReleaseURL(d.repo, d.version) + "/layers/" + layer.ID + ".squashfs"
	destPath := filepath.Join(cacheDir, layer.ID+".squashfs")
	return d.downloadWithRetry(layerURL, destPath)
}

// importLayer imports a downloaded layer into the volume manager
func (d *Downloader) importLayer(layer *ct.ImageLayer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	fs := &volume.Filesystem{
		ID:   layer.ID,
		Data: f,
		Size: info.Size(),
		Type: volume.VolumeTypeSquashfs,
		Meta: layer.Meta,
	}

	_, err = d.vman.ImportFilesystem("default", fs)
	return err
}
