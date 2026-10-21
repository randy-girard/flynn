package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func init() {
	Register("domain", runDomainShow, `
usage: flynn-host domain

Show the cluster domain, the app domain, and which app serves the apex.

Flynn itself (dashboard, git, controller) stays on the cluster domain.
App hostnames are <app>.<app-domain>. Those match unless you set a
different app domain:

    $ flynn-host domain:apps flynnapps.com

The apex is the cluster domain itself (flynndashboard.com) with no
subdomain. Only one app can own it.
`)
	Register("domain:apps", runDomainApps, `
usage: flynn-host domain:apps [<domain>]
       flynn-host domain:apps --reset

Set the domain used for every app hostname, including preview apps.

Flynn can run on flynndashboard.com while apps are served at
<app>.flynnapps.com. With no argument, print the current app domain.
--reset uses the cluster domain again. Existing routes are left as they are;
new apps and preview apps use the new domain.

Examples:

    $ flynn-host domain:apps
    $ flynn-host domain:apps flynnapps.com
    $ flynn-host domain:apps --reset

Options:
    --reset  Use the cluster domain for new app hostnames
`)
	Register("domain:apex", runDomainApex, `
usage: flynn-host domain:apex [<app>]
       flynn-host domain:apex --clear

Choose which app serves the apex (root) hostname.

The www plugin registers both www.<domain> and the apex on install. Change
it later with:

    $ flynn-host domain:apex www
    $ flynn-host domain:apex dashboard
    $ flynn-host domain:apex --clear

Options:
    --clear  Remove the apex HTTP route
`)
}

func runDomainShow(_ *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	domain, err := clusterApexDomain(client)
	if err != nil {
		return err
	}
	return printApex(client, domain)
}

func runDomainApps(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	if args.Bool["--reset"] {
		if err := client.SetAppDomain(""); err != nil {
			return err
		}
	} else if domain := strings.TrimSpace(args.String["<domain>"]); domain != "" {
		if err := client.SetAppDomain(domain); err != nil {
			return err
		}
	}
	view, err := client.GetAppDomain()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "CLUSTER DOMAIN", view.ClusterDomain)
	listRec(w, "APP DOMAIN", view.Effective)
	if strings.TrimSpace(view.Domain) == "" {
		listRec(w, "APP DOMAIN SET", "cluster default")
	} else {
		listRec(w, "APP DOMAIN SET", view.Domain)
	}
	return nil
}

func runDomainApex(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	domain, err := clusterApexDomain(client)
	if err != nil {
		return err
	}
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
	if view, err := client.GetAppDomain(); err == nil && view != nil {
		listRec(w, "APP DOMAIN", view.Effective)
	}
	if info == nil || info.App == nil {
		listRec(w, "APEX APP", "(none)")
		fmt.Fprintf(os.Stdout, "Assign with: flynn-host domain:apex <app>\n")
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
