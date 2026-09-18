package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("domain", runDomain, `
usage: flynn-host domain
       flynn-host domain apex [<app>]
       flynn-host domain apex --clear

Show the cluster domain, or choose which app serves the apex (root) hostname.

Apps are normally reached at <app>.<cluster-domain> (for example
www.flynncluster.com). The apex is the cluster domain itself
(flynncluster.com) with no subdomain. Only one app can own it.

The www plugin registers both www.<domain> and the apex on install. Change
it later with:

    $ flynn-host domain apex www
    $ flynn-host domain apex dashboard
    $ flynn-host domain apex --clear

Options:
    --clear  Remove the apex HTTP route
`)
}

func runDomain(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	domain, err := clusterApexDomain(client)
	if err != nil {
		return err
	}
	if args.Bool["apex"] {
		if args.Bool["--clear"] {
			apps, err := client.AppList()
			if err != nil {
				return err
			}
			info, err := plugin.ClearApex(client, apps, domain)
			if err != nil {
				return err
			}
			if info == nil || info.App == nil {
				fmt.Printf("No apex route on %s.\n", domain)
				return nil
			}
			fmt.Printf("Removed apex %s from %s.\n", domain, info.App.Name)
			return nil
		}
		appName := strings.TrimSpace(args.String["<app>"])
		if appName == "" {
			return printApex(client, domain)
		}
		apps, err := client.AppList()
		if err != nil {
			return err
		}
		app, err := findDomainApp(apps, appName)
		if err != nil {
			return err
		}
		route, err := plugin.AssignApex(client, apps, domain, app)
		if err != nil {
			return err
		}
		fmt.Printf("%s now serves %s (%s).\n", app.Name, domain, route.FormattedID())
		return nil
	}
	return printApex(client, domain)
}

func printApex(client controller.Client, domain string) error {
	apps, err := client.AppList()
	if err != nil {
		return err
	}
	info, err := plugin.LookupApex(client, apps, domain)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "CLUSTER DOMAIN", domain)
	if info == nil || info.App == nil {
		listRec(w, "APEX APP", "(none)")
		fmt.Fprintf(os.Stdout, "Assign with: flynn-host domain apex <app>\n")
		return nil
	}
	listRec(w, "APEX APP", info.App.Name)
	if info.Route != nil {
		listRec(w, "APEX ROUTE", info.Route.FormattedID())
	}
	return nil
}

func clusterApexDomain(client controller.Client) (string, error) {
	release, err := client.GetAppRelease("controller")
	if err != nil {
		return "", fmt.Errorf("could not determine cluster domain from controller")
	}
	domain := strings.TrimSpace(release.Env["DEFAULT_ROUTE_DOMAIN"])
	if domain == "" {
		domain = strings.TrimSpace(release.Env["CLUSTER_DOMAIN"])
	}
	if domain == "" {
		return "", fmt.Errorf("could not determine cluster domain from controller")
	}
	return domain, nil
}

func findDomainApp(apps []*ct.App, name string) (*ct.App, error) {
	if app, err := plugin.LookupPluginApp(apps, name); err == nil {
		return app, nil
	}
	for _, app := range apps {
		if app != nil && (app.Name == name || app.ID == name) {
			return app, nil
		}
	}
	return nil, fmt.Errorf("app %q not found", name)
}
