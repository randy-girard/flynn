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
	"github.com/flynn/flynn/pkg/verify"
	"github.com/inconshreveable/log15"
)

const (
	maxDownloadRetries  = 5
	initialRetryDelay   = 2 * time.Second
	maxRetryDelay       = 30 * time.Second
	retryBackoffFactor  = 2
)

// binaries maps the asset name in the release to the local binary name
// The release uses OS/arch suffixed names for host binaries
var binaries = map[string]string{
	"flynn-host-linux-amd64": "flynn-host",
	"flynn-linux-amd64":      "flynn-linux-amd64",
	"flynn-init-linux-amd64": "flynn-init",
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
	for assetName, localName := range binaries {
		path, err := d.downloadGzippedBinary(assetName, localName, dir)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0755); err != nil {
			return nil, err
		}
		paths[localName] = path
	}
	// symlink flynn to flynn-linux-amd64
	if err := symlink("flynn-linux-amd64."+d.version, filepath.Join(dir, "flynn")); err != nil {
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
		path, err := d.downloadGzippedFile(conf, dir)
		if err != nil {
			return nil, err
		}
		paths[conf] = path
	}
	return paths, nil
}

// downloadWithRetry wraps the download with exponential backoff retry logic.
// This helps handle transient GitHub 500 errors, especially when multiple
// cluster nodes are downloading layers simultaneously.
func (d *Downloader) downloadWithRetry(assetURL, destPath string) error {
	var lastErr error
	delay := initialRetryDelay
	for attempt := 1; attempt <= maxDownloadRetries; attempt++ {
		err := d.client.DownloadFile(assetURL, destPath)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < maxDownloadRetries {
			d.log.Warn("download failed, retrying", "attempt", attempt, "delay", delay, "err", err)
			time.Sleep(delay)
			delay *= retryBackoffFactor
			if delay > maxRetryDelay {
				delay = maxRetryDelay
			}
		}
	}
	return fmt.Errorf("download failed after %d attempts: %s", maxDownloadRetries, lastErr)
}

// downloadGzippedBinary downloads a gzipped binary from GitHub releases, decompresses it,
// and creates a versioned file with a symlink. The assetName is the name in the release
// (e.g., flynn-host-linux-amd64) and localName is the local binary name (e.g., flynn-host).
func (d *Downloader) downloadGzippedBinary(assetName, localName, dir string) (string, error) {
	// Construct the asset URL
	gzName := assetName + ".gz"
	assetURL := ghrelease.GetReleaseURL(d.repo, d.version) + "/" + gzName

	// Download to temp file
	tmpPath := filepath.Join(dir, gzName+".tmp")
	if err := d.downloadWithRetry(assetURL, tmpPath); err != nil {
		return "", fmt.Errorf("error downloading %s: %s", assetName, err)
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
		return "", fmt.Errorf("error creating gzip reader for %s: %s", assetName, err)
	}
	defer gz.Close()

	// Destination path with version suffix
	destPath := filepath.Join(dir, localName+"."+d.version)

	// Write decompressed content
	destFile, err := os.Create(destPath)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(destFile, gz); err != nil {
		destFile.Close()
		os.Remove(destPath)
		return "", fmt.Errorf("error decompressing %s: %s", assetName, err)
	}
	destFile.Close()

	// Create symlink from localName to versioned file
	if err := symlink(filepath.Base(destPath), filepath.Join(dir, localName)); err != nil {
		return "", err
	}

	return destPath, nil
}

// downloadGzippedFile downloads a gzipped file from GitHub releases and decompresses it.
// Used for config files that don't need versioning.
func (d *Downloader) downloadGzippedFile(name, dir string) (string, error) {
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

	// Destination path (no versioning for config files)
	destPath := filepath.Join(dir, name)

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

	return destPath, nil
}

// symlink creates a symlink, removing any existing file/symlink first
func symlink(target, link string) error {
	os.Remove(link)
	return os.Symlink(target, link)
}

// DownloadImagesManifest downloads the images manifest and returns the images map
// without downloading layers. This is useful for updating system apps.
func (d *Downloader) DownloadImagesManifest(configDir string) (map[string]*ct.Artifact, error) {
	// Download images manifest
	manifestURL := ghrelease.GetReleaseURL(d.repo, d.version) + "/images.json.gz"
	manifestPath := filepath.Join(configDir, "images.json.gz.tmp")
	if err := d.downloadWithRetry(manifestURL, manifestPath); err != nil {
		return nil, fmt.Errorf("error downloading images manifest: %s", err)
	}
	defer os.Remove(manifestPath)

	// Decompress manifest
	gzFile, err := os.Open(manifestPath)
	if err != nil {
		return nil, err
	}
	defer gzFile.Close()

	gz, err := gzip.NewReader(gzFile)
	if err != nil {
		return nil, fmt.Errorf("error creating gzip reader for images manifest: %s", err)
	}
	defer gz.Close()

	// Parse manifest
	var images map[string]*ct.Artifact
	if err := json.NewDecoder(gz).Decode(&images); err != nil {
		return nil, fmt.Errorf("error parsing images manifest: %s", err)
	}

	return images, nil
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
				// Check if layer already exists and has the expected size.
				// A truncated file (from a previous interrupted download)
				// must be re-downloaded to avoid "verify: data too short"
				// errors when the layer is later mounted.
				layerPath := filepath.Join(layerCacheDir, layer.ID+".squashfs")
				if fi, err := os.Stat(layerPath); err == nil {
					if layer.Length > 0 && fi.Size() != layer.Length {
						d.log.Warn("cached layer has wrong size, re-downloading", "layer", layer.ID, "expected", layer.Length, "actual", fi.Size())
						os.Remove(layerPath)
					} else {
						continue // Layer already cached
					}
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

				// Import layer into volume manager (best-effort).
				// During a zero-downtime daemon restart, the volume
				// manager's DB may be temporarily closed. Since the
				// layer file is already on disk, the import can safely
				// be skipped — the volume manager will discover it on
				// the next restart or when the layer is first used.
				if d.vman != nil {
					if err := d.importLayer(layer, layerPath); err != nil {
						if err == volumemanager.ErrDBClosed || err == volumemanager.ErrVolumeExists {
							d.log.Warn("skipping layer import", "layer", layer.ID, "reason", err)
						} else {
							return fmt.Errorf("error importing layer %s: %s", layer.ID, err)
						}
					}
				}
			}
		}
	}

	return nil
}

// downloadLayer downloads a single layer from GitHub releases and verifies
// its integrity using the expected size and cryptographic hashes from the
// image manifest. If verification fails, the file is deleted and the
// download is retried with exponential backoff.
func (d *Downloader) downloadLayer(layer *ct.ImageLayer, cacheDir string) error {
	layerURL := ghrelease.GetReleaseURL(d.repo, d.version) + "/" + layer.ID + ".squashfs"
	destPath := filepath.Join(cacheDir, layer.ID+".squashfs")

	var lastErr error
	delay := initialRetryDelay
	for attempt := 1; attempt <= maxDownloadRetries; attempt++ {
		if attempt > 1 {
			d.log.Warn("retrying layer download", "layer", layer.ID, "attempt", attempt, "delay", delay, "err", lastErr)
			time.Sleep(delay)
			delay *= retryBackoffFactor
			if delay > maxRetryDelay {
				delay = maxRetryDelay
			}
		}

		if err := d.client.DownloadFile(layerURL, destPath); err != nil {
			lastErr = err
			continue
		}

		// Verify the downloaded file against expected size and hashes
		if err := verifyLayerFile(destPath, layer.Length, layer.Hashes); err != nil {
			d.log.Warn("layer verification failed, deleting and retrying", "layer", layer.ID, "err", err)
			os.Remove(destPath)
			lastErr = err
			continue
		}

		return nil
	}
	return fmt.Errorf("download failed after %d attempts: %s", maxDownloadRetries, lastErr)
}

// verifyLayerFile opens a downloaded layer file and verifies its size and
// cryptographic hashes match the expected values from the image manifest.
// Returns nil if no verification data is available (size <= 0 or no hashes).
func verifyLayerFile(path string, expectedSize int64, hashes map[string]string) error {
	if expectedSize <= 0 || len(hashes) == 0 {
		return nil // no verification data available
	}
	v, err := verify.NewVerifier(hashes, expectedSize)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(io.Discard, v.Reader(f)); err != nil {
		return err
	}
	return v.Verify()
}

// DownloadImageLayers downloads layers for a set of images from GitHub releases.
// This is used during updates to ensure layers are available before deploying.
func (d *Downloader) DownloadImageLayers(images map[string]*ct.Artifact, log log15.Logger) error {
	layerCacheDir := "/var/lib/flynn/layer-cache"
	if err := os.MkdirAll(layerCacheDir, 0755); err != nil {
		return fmt.Errorf("error creating layer cache dir: %s", err)
	}

	// Track unique layers to avoid downloading duplicates
	downloadedLayers := make(map[string]bool)

	for name, artifact := range images {
		manifest := artifact.Manifest()
		if manifest == nil {
			continue
		}

		for _, rootfs := range manifest.Rootfs {
			for _, layer := range rootfs.Layers {
				// Skip if already downloaded in this session
				if downloadedLayers[layer.ID] {
					continue
				}

				// Check if layer already exists on disk and has the expected size.
				// A truncated file must be re-downloaded.
				layerPath := filepath.Join(layerCacheDir, layer.ID+".squashfs")
				if fi, err := os.Stat(layerPath); err == nil {
					if layer.Length > 0 && fi.Size() != layer.Length {
						log.Warn("cached layer has wrong size, re-downloading", "layer", layer.ID, "expected", layer.Length, "actual", fi.Size())
						os.Remove(layerPath)
					} else {
						downloadedLayers[layer.ID] = true
						continue // Layer already cached
					}
				}

				log.Info("downloading layer", "image", name, "layer", layer.ID)
				if err := d.downloadLayer(layer, layerCacheDir); err != nil {
					return fmt.Errorf("error downloading layer %s for image %s: %s", layer.ID, name, err)
				}
				downloadedLayers[layer.ID] = true
			}
		}
	}

	return nil
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
