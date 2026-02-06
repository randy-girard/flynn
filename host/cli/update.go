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

Update Flynn components using GitHub releases.`)
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
