package main

import (
	"fmt"
	"io"
	"os"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func init() {
	register("plugin:list", runPlugins, `
usage: flynn plugin:list [--known] [--check]

List plugins installed on the current cluster, including the installed VERSION
(GitHub tag). --check queries GitHub for the highest compatible tag for this
Flynn version and shows UPDATE and STATUS (current, update, or -).

--known prints the public first-party catalog Flynn knows how to install (name,
GitHub repo, description) without talking to the cluster. Operators install
those names with flynn-host plugin:install. Private first-party plugins (for
example enterprise) are listed in a footer; they exist but cannot be installed
from this catalog. flynn-host plugin:list --known is the same output.

The installed list comes from the controller (plugin apps the credential can
see), not from a local checkout. After install, plugin CLI commands also
appear in flynn help when the plugin is a resource provider (or sets
cli.user). kind: app system plugins are listed here but are not user flynn
commands.
`)
}

func runPlugins(args *docopt.Args) error {
	if args != nil && args.Bool["--known"] {
		return plugin.WriteKnownPlugins(os.Stdout, plugin.DefaultGitHubOrg(), plugin.KnownPlugins())
	}
	client, err := getClusterClient()
	if err != nil {
		return err
	}
	apps, err := client.AppList()
	if err != nil {
		return err
	}
	check := args != nil && args.Bool["--check"]
	printPluginList(os.Stdout, os.Stderr, apps, check)
	return nil
}

func printPluginList(stdout, stderr io.Writer, apps []*ct.App, check bool) {
	installed := plugin.ListInstalled(apps)
	var rows []plugin.ListedPlugin
	if check {
		in := &plugin.Installer{Stdout: stdout, Stderr: stderr}
		rows = in.CheckUpdates(installed, "")
	} else {
		rows = plugin.ListedFromInstalled(installed)
	}
	_ = plugin.WriteUserPluginTable(stdout, rows, check)
	if len(rows) == 0 {
		fmt.Fprintln(stderr, "no plugins installed")
	}
	if check {
		plugin.WritePluginUpdateNotes(stderr, rows)
	}
}
