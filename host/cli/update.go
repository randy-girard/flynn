package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/installsource"
)

func init() {
	Register("update", runUpdate, `
usage: flynn-host update [options]

Options:
  -b --bin-dir=<dir>             directory to download binaries to [default: /usr/local/bin]
  -c --config-dir=<dir>          directory to download config files to [default: /etc/flynn]
  --github-repo=<repo>           GitHub repository for updates [default: randy-girard/flynn]
  --check                        only check for updates, don't install (cached for 1h; --force refreshes)
  --version=<ver>                update to a specific version
  --force                        re-run an update even if flynn-host is already the latest version; with --check, refresh the GitHub lookup cache
  --no-restart                   only download binaries, don't restart the daemon
  --skip-images                  skip updating container images and system apps
  --images-only                  only update container images and system apps (skip binaries)
  --tarball=<path>               update from a local tarball instead of GitHub
  --cleanup-tarball              remove the source tarball after a successful
                                 update once binaries are installed and any
                                 cluster-wide rollout from the extracted contents
                                 has finished (the tarball itself is not needed
                                 after extraction)
  --all-nodes                    update the entire cluster: push binaries to other
                                 hosts, pull images on every node, deploy system apps.
                                 Without this flag, only this host is updated (binaries
                                 locally; no cluster-wide image rollout).
  --health-timeout=<duration>    per-host wait for the cluster to report healthy
                                 between rolling restarts (e.g. 10m). Larger clusters
                                 or slow sirenia replication may need a longer timeout
                                 than the default.
  --inter-host-delay=<duration>  extra settle delay after a host is healthy before
                                 starting the next host's restart, to let the
                                 scheduler observe the restarted host coming back up
                                 and re-place jobs onto it (e.g. 30s).
  --wait-jobs-timeout=<duration> per-host wait for the scheduler to place at least
                                 one app job back on the freshly restarted host before
                                 continuing. Non-fatal: logs a warning and continues
                                 on timeout (e.g. 3m).
  --restart-down-jobs            during sirenia cluster quorum repair, also restart
                                 database peer jobs that are in the down state by
                                 re-asserting the formation so the scheduler replaces
                                 them. Without this flag only unregistered or unhealthy
                                 up/starting jobs are restarted.

Update Flynn components using GitHub releases or a local tarball.

A GitHub version bump installs the new flynn-host binary first, then re-execs
it so flynn-init, the CLI, daemon restart, and image rollout run with the new
updater. That continuation does not need --force. Use --force only to repeat
an update when this host is already on the target version.

After downloading new binaries, the running flynn-host daemon is restarted via
systemctl. Job containers normally survive (KillMode=process); discoverd
registration and sirenia leaders still need time to settle between hosts.
Use --no-restart to skip the restart and handle it manually.

By default this command updates flynn-host/flynn-init on this machine only.
Use --all-nodes to roll the same binaries out to every cluster host, pull new
container layers everywhere, and deploy updated system apps. Until then, image
and system-app updates are skipped so you can update hosts manually in any order.

Use --skip-images with --all-nodes to update binaries on every node without
touching container images. --images-only requires --all-nodes (image rollout is
always cluster-wide).

User apps with no current release (created in the dashboard or with
flynn create but never deployed) are skipped so the update can finish.
docker:push and container-stack apps keep their image; only slugrunner
git apps are refreshed to the new slugrunner.

When --tarball is specified, the update is performed from a local .tar.gz file
(the same tarball produced by the release scripts) instead of GitHub. With
--all-nodes, a temporary HTTP server is started on this node to serve the
tarball contents to other cluster nodes.`)
	Register("rollback", runRollback, `
usage: flynn-host rollback --version=<ver> [options]

Options:
  -b --bin-dir=<dir>             directory to download binaries to [default: /usr/local/bin]
  -c --config-dir=<dir>          directory to download config files to [default: /etc/flynn]
  --github-repo=<repo>           GitHub repository [default: randy-girard/flynn]
  --version=<ver>                Flynn GitHub release tag to restore (required)
  --no-restart                   only download binaries, don't restart the daemon
  --skip-images                  skip updating container images and system apps
  --images-only                  only roll back container images and system apps
  --tarball=<path>               restore from a local release tarball instead of GitHub
  --this-host                    only this machine (default is every cluster host)
  --health-timeout=<duration>    per-host wait for the cluster to report healthy
  --inter-host-delay=<duration>  extra settle delay after a host is healthy
  --wait-jobs-timeout=<duration> per-host wait for jobs to land on a restarted host
  --restart-down-jobs            also restart down database peer jobs during quorum repair

Install a previously published Flynn release on this cluster. This is the
supported rollback after a bad in-place upgrade: same path as
flynn-host update --version --all-nodes --force.

It does not undo user app deploys, postgres/volume data, blobstore objects,
or plugin databases. After the platform is on the older tag, run
flynn-host plugin:update-all so plugins match that Flynn calver.
`)
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

	// Apply per-invocation overrides for the rolling-restart resilience
	// knobs. Defaults stay in github_updater.go so the constants remain
	// the single source of truth; flags only kick in when supplied.
	if err := applyUpdateTimingFlags(args, log); err != nil {
		return err
	}

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

func runRollback(args *docopt.Args) error {
	if strings.TrimSpace(args.String["--version"]) == "" && strings.TrimSpace(args.String["--tarball"]) == "" {
		return fmt.Errorf("rollback requires --version (GitHub tag) or --tarball")
	}
	if args.Bool == nil {
		args.Bool = map[string]bool{}
	}
	args.Bool["--force"] = true
	if !args.Bool["--this-host"] {
		args.Bool["--all-nodes"] = true
	}
	fmt.Fprint(os.Stderr, `Rolling Flynn back to a previous platform release.
This restores host binaries and (unless --skip-images) system-app images.
It does not roll back user app releases, postgres data, volumes, blobstore
objects, or plugin databases. Afterward run: sudo flynn-host plugin:update-all
`)
	return runUpdate(args)
}

// applyUpdateTimingFlags parses the optional --health-timeout,
// --inter-host-delay and --wait-jobs-timeout flags and overrides the
// package-level defaults in github_updater.go. Empty/missing values are
// left at their defaults; invalid durations return an error so the user
// notices the typo before the long-running update starts.
func applyUpdateTimingFlags(args *docopt.Args, log log15.Logger) error {
	parse := func(name string, target *time.Duration) error {
		raw, ok := args.String[name]
		if !ok || raw == "" {
			return nil
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid value for %s: %w", name, err)
		}
		if d <= 0 {
			return fmt.Errorf("invalid value for %s: must be positive", name)
		}
		*target = d
		log.Info("override update timing", "flag", name, "value", d)
		return nil
	}
	if err := parse("--health-timeout", &updateHealthTimeout); err != nil {
		return err
	}
	if err := parse("--inter-host-delay", &updateInterHostDelay); err != nil {
		return err
	}
	if err := parse("--wait-jobs-timeout", &updateWaitJobsTimeout); err != nil {
		return err
	}
	return nil
}
