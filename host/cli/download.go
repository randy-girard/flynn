package cli

import (
	"fmt"
	"os"
	"runtime"

	"github.com/docker/go-units"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/downloader"
	"github.com/flynn/flynn/host/volume/zfs"
	"github.com/flynn/flynn/pkg/ghrelease"
	"github.com/flynn/flynn/pkg/installsource"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"

	volumemanager "github.com/flynn/flynn/host/volume/manager"
)

func init() {
	Register("download", runDownload, `
usage: flynn-host download [options]

Options:
  -b --bin-dir=<dir>       directory to download binaries to [default: /usr/local/bin]
  -c --config-dir=<dir>    directory to download config files to [default: /etc/flynn]
  -v --volpath=<path>      directory to create volumes in [default: /var/lib/flynn/volumes]
  --github-repo=<repo>     GitHub repository for downloads [default: randy-girard/flynn]
  --version=<ver>          version to download (defaults to latest release)
  --zpool=<name>           name of ZFS pool to use [default: flynn-default]

Download Flynn binaries, config and images from GitHub releases.`)
}

func runDownload(args *docopt.Args) error {
	log := log15.New()

	binDir := args.String["--bin-dir"]
	configDir := args.String["--config-dir"]
	volPath := args.String["--volpath"]
	zpoolName := args.String["--zpool"]
	repo := args.String["--github-repo"]
	targetVersion := args.String["--version"]

	// Determine version to download
	client := ghrelease.NewClient(repo, log)
	var downloadVersion string
	if targetVersion != "" {
		downloadVersion = targetVersion
		log.Info("using specified version", "version", downloadVersion)
	} else {
		log.Info("fetching latest release from GitHub", "repo", repo)
		release, err := client.GetLatestRelease()
		if err != nil {
			log.Error("failed to get latest release", "err", err)
			return err
		}
		downloadVersion = release.TagName
		log.Info("found latest release", "version", downloadVersion)
	}

	// Initialize ZFS volume manager
	log.Info("initializing ZFS volume manager", "zpool", zpoolName)
	vman, err := initVolumeManager(volPath, zpoolName, log)
	if err != nil {
		log.Error("error initializing volume manager", "err", err)
		return err
	}

	// Create downloader
	d := downloader.New(repo, vman, downloadVersion, log)

	// Download binaries
	log.Info("downloading binaries", "dir", binDir)
	binPaths, err := d.DownloadBinaries(binDir)
	if err != nil {
		log.Error("error downloading binaries", "err", err)
		return err
	}
	for name, path := range binPaths {
		log.Info("downloaded binary", "name", name, "path", path)
	}

	// Download config
	log.Info("downloading config", "dir", configDir)
	configPaths, err := d.DownloadConfig(configDir)
	if err != nil {
		log.Error("error downloading config", "err", err)
		return err
	}
	for name, path := range configPaths {
		log.Info("downloaded config", "name", name, "path", path)
	}

	// Download images
	log.Info("downloading images")
	ch := make(chan *ct.ImagePullInfo)
	go func() {
		for info := range ch {
			switch info.Type {
			case ct.ImagePullTypeImage:
				log.Info("downloading image", "name", info.Name)
			case ct.ImagePullTypeLayer:
				log.Info(fmt.Sprintf("downloading layer %s (%s)",
					info.Layer.ID, units.BytesSize(float64(info.Layer.Length))))
			}
		}
	}()
	if err := d.DownloadImages(configDir, ch); err != nil {
		log.Error("error downloading images", "err", err)
		return err
	}

	// Record installation source
	source := installsource.NewGitHubSource(repo, downloadVersion)
	if err := installsource.Save(configDir, source); err != nil {
		log.Warn("failed to save install-source.json", "err", err)
	}

	log.Info("download complete", "version", downloadVersion)
	fmt.Printf("Flynn %s downloaded successfully from GitHub (%s)\n", downloadVersion, repo)
	return nil
}

func initVolumeManager(volPath, zpoolName string, log log15.Logger) (*volumemanager.Manager, error) {
	if runtime.GOOS != "linux" {
		log.Warn("ZFS volume manager only available on Linux, skipping layer import")
		return nil, nil
	}

	// Create volume path if it doesn't exist
	if err := os.MkdirAll(volPath, 0755); err != nil {
		return nil, fmt.Errorf("error creating volume path: %s", err)
	}

	// Initialize ZFS provider
	provider, err := zfs.NewProvider(&zfs.ProviderConfig{
		DatasetName: zpoolName,
		Make:        nil,
		WorkingDir:  volPath,
	})
	if err != nil {
		return nil, fmt.Errorf("error initializing ZFS provider: %s", err)
	}

	// Create volume manager
	vman := volumemanager.New(volPath+"/volumes.bolt", log, nil)
	if err := vman.AddProvider("default", provider); err != nil {
		return nil, fmt.Errorf("error adding volume provider: %s", err)
	}

	return vman, nil
}
