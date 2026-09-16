package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
)

func init() {
	register("plugins", runPlugins, `
usage: flynn plugins

List plugins installed on the current cluster.

The list comes from the controller (plugin apps the credential can see), not
from a local checkout. Operators install with flynn-host plugin install.
After install, plugin CLI commands also appear in flynn help when the
plugin is a resource provider (or sets cli.user). kind: app system plugins
are listed here but are not user flynn commands.
`)
}

func runPlugins(_ *docopt.Args, client controller.Client) error {
	apps, err := client.AppList()
	if err != nil {
		return err
	}
	printPluginList(os.Stdout, os.Stderr, apps)
	return nil
}

func printPluginList(stdout, stderr io.Writer, apps []*ct.App) {
	n := writePluginTable(stdout, apps)
	if n == 0 {
		fmt.Fprintln(stderr, "no plugins installed")
	}
}

func writePluginTable(w io.Writer, apps []*ct.App) int {
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	defer tw.Flush()
	fmt.Fprintln(tw, "NAME\tKIND\tCOMMAND\tUSAGE\tREF")
	n := 0
	for _, p := range plugin.ListInstalled(apps) {
		cmd, usage := "", ""
		if p.CLI != nil && p.CLI.UserVisible(p.Kind) {
			cmd = p.CLI.Command
			usage = p.CLI.Usage
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Kind, cmd, usage, p.Ref)
		n++
	}
	return n
}
