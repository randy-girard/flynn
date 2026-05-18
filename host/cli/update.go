package cli

import (
	"fmt"

	"github.com/flynn/flynn/pkg/installsource"
	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
)

func init() {
	Register("update", runUpdate, `
usage: flynn-host update [options]

Options:
  -b --bin-dir=<dir>       directory to download binaries to [default: /usr/local/bin]
  -c --config-dir=<dir>    directory to download config files to [default: /etc/flynn]
  --github-repo=<repo>     GitHub repository for updates [default: randy-girard/flynn]
  --check                  only check for updates, don't install
  --version=<ver>          update to a specific version
  --force                  force update even if already on the latest version
  --no-restart             only download binaries, don't restart the daemon
  --skip-images            skip updating container images and system apps
  --images-only            only update container images and system apps (skip binaries)
  --tarball=<path>         update from a local tarball instead of GitHub
  --all-nodes              update the entire cluster: push binaries to other
                           hosts, pull images on every node, deploy system apps.
                           Without this flag, only this host is updated (binaries
                           locally; no cluster-wide image rollout).

Update Flynn components using GitHub releases or a local tarball.

After downloading new binaries, the running flynn-host daemon is automatically
restarted using a zero-downtime handoff. Use --no-restart to skip the restart
and handle it manually (e.g. via systemctl restart flynn-host).

By default this command updates flynn-host/flynn-init on this machine only.
Use --all-nodes to roll the same binaries out to every cluster host, pull new
container layers everywhere, and deploy updated system apps. Until then, image
and system-app updates are skipped so you can update hosts manually in any order.

Use --skip-images with --all-nodes to update binaries on every node without
touching container images. --images-only requires --all-nodes (image rollout is
always cluster-wide).

When --tarball is specified, the update is performed from a local .tar.gz file
(the same tarball produced by the release scripts) instead of GitHub. With
--all-nodes, a temporary HTTP server is started on this node to serve the
tarball contents to other cluster nodes.`)
}

// minVersion is the minimum version that can be updated from.
//
// The current minimum version is v20161108.0 since versions prior to that used
// a different image format
var minVersion = "v20161108.0"

var ErrIncompatibleVersion = fmt.Errorf(`
Versions prior to %s cannot be updated in-place to this version of Flynn.
In order to update to this version a cluster backup/restore is required.
Please see the updating documentation at https://flynn.io/docs/production#backup/restore.
`[1:], minVersion)

func runUpdate(args *docopt.Args) error {
	log := log15.New()
	configDir := args.String["--config-dir"]

	// If --tarball is specified, use tarball-based update
	if tarballPath := args.String["--tarball"]; tarballPath != "" {
		return runTarballUpdate(args, tarballPath, configDir, log)
	}

	// Get repository from install-source.json or use default
	repo := args.String["--github-repo"]
	installSource, err := installsource.Load(configDir)
	if err == nil {
		log.Info("detected installation source", "source", installSource.Source, "version", installSource.Version)
		if installSource.Repository != "" && repo == "randy-girard/flynn" {
			// Use the repository from install-source.json if not explicitly overridden
			repo = installSource.Repository
		}
	} else {
		log.Info("no install-source.json found, using default repository", "repo", repo)
	}

	return runGitHubUpdate(args, repo, configDir, log)
}
