package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/appmanifest"
)

func applyCreateManifest(client controller.Client, app *ct.App, path string) error {
	raw, err := readManifest(path)
	if err != nil {
		return err
	}
	ap, err := appmanifest.New(client, os.Stdout, false)
	if err != nil {
		return err
	}
	ap.Hooks = appmanifest.Hooks{
		GitHubInstalled: func() bool {
			return ap.Catalog != nil && ap.Catalog.Lookup("github") != nil
		},
		ConnectGitHub: func(appID string, spec appmanifest.GitHub) error {
			return connectManifestGitHub(app.Name, spec)
		},
	}
	return ap.ApplyCreate(app, raw, appmanifest.PrepareOptions{
		Placeholders: map[string]string{"app": app.Name},
	}, func(tag string) error {
		return pushManifestImage(client, app.Name, tag)
	})
}

func readManifest(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func connectManifestGitHub(appName string, spec appmanifest.GitHub) error {
	prev := flagApp
	flagApp = appName
	defer func() { flagApp = prev }()
	args := []string{"connect", spec.Repository}
	if spec.Branch != "" {
		args = append(args, "--branch="+spec.Branch)
	}
	if spec.AutoDeploy {
		args = append(args, "--auto-deploy")
	}
	if spec.WaitChecks {
		args = append(args, "--wait-checks")
	}
	return runPluginCommand("github", args)
}

func pushManifestImage(client controller.Client, appName, tag string) error {
	prev := flagApp
	flagApp = appName
	defer func() { flagApp = prev }()
	return runDockerPushTar(&docopt.Args{String: map[string]string{"<image>": tag}}, client)
}

func appIsProtected(client controller.Client, name string) bool {
	app, err := client.GetApp(name)
	if err != nil || app == nil {
		return false
	}
	return appmanifest.Protected(app)
}

func protectedScaleError(processes map[string]int) error {
	for typ, n := range processes {
		if n == 0 {
			return fmt.Errorf("app is protected and cannot be scaled to zero (%s=0)", typ)
		}
	}
	return nil
}
