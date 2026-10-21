package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/flynn/go-docopt"
	cfg "github.com/randy-girard/flynn/cli/config"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	register("git:remote", runRemote, `
usage: flynn git:remote [<remote>] [-y]

Create a git remote that allows deploying the application via git.
If a name for the remote is not provided 'flynn' will be used.
The reserved name preview adds the pipeline preview remote (when
preview apps are enabled) so git push preview <branch> creates a
preview app.

Note that the -a <app> option must be given so the remote to add is known.

Options:
	-y, --yes              Skip the confirmation prompt if the git remote already exists.

Examples:

	$ flynn -a turkeys-stupefy-perry git:remote
	Created remote flynn with url https://git.dev.localflynn.com/turkeys-stupefy-perry.git

	$ flynn -a turkeys-stupefy-perry git:remote staging
	Created remote staging with url https://git.dev.localflynn.com/turkeys-stupefy-perry.git
`)
}

func runRemote(args *docopt.Args, client controller.Client) error {
	app, err := client.GetApp(mustApp())
	if err != nil {
		return err
	}

	remote := args.String["<remote>"]
	if remote == "" {
		remote = "flynn"
	}

	url := gitURL(clusterConf, app.Name)
	if remote == previewRemoteName {
		previewURL, err := previewRemoteURL(clusterConf, app)
		if err != nil {
			return err
		}
		url = previewURL
	}

	if !inGitRepo() {
		log.Print("Must be executed within a git repository.")
		return nil
	}

	if !args.Bool["--yes"] {
		update, err := promptReplaceRemote(remote)
		if err != nil {
			return err
		}
		if update == false {
			return nil
		}
	}

	// Register git remote
	exec.Command("git", "remote", "remove", remote).Run()
	exec.Command("git", "remote", "add", "--", remote, url).Run()

	log.Printf("Created remote %s with url %s.", remote, url)
	return nil
}

const (
	appMetaPipeline      = "flynn-pipeline"
	appMetaPreviewRemote = "flynn-preview-remote"
)

func previewRemoteURL(conf *cfg.Cluster, app *ct.App) (string, error) {
	if app == nil {
		return "", fmt.Errorf("app is required")
	}
	pipeline := ""
	enabled := ""
	if app.Meta != nil {
		pipeline = strings.TrimSpace(app.Meta[appMetaPipeline])
		enabled = strings.TrimSpace(app.Meta[appMetaPreviewRemote])
	}
	if pipeline == "" {
		return "", fmt.Errorf("app %s is not in a pipeline; the preview remote is not available", app.Name)
	}
	if enabled != "true" {
		return "", fmt.Errorf("preview apps are not enabled on pipeline %s; the preview remote is not available", pipeline)
	}
	if conf == nil || strings.TrimSpace(conf.GitURL) == "" {
		return "", fmt.Errorf("cluster git URL is not configured")
	}
	return gitPreviewURL(conf, pipeline), nil
}
