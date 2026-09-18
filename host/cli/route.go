package cli

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/flynn/go-docopt"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	Register("route:add", runHostRouteAdd, `
usage: flynn-host route:add http --app <app> [-s <service>] [-p <port>] [--sticky] [--leader] <domain>

Add an HTTP route, including path-based routes such as example.com/admin.
This is a cluster-admin operation; the Flynn CLI cannot create path routes.

Options:
    --app=<app>              target app name or ID
    -s, --service=<service>  service name (defaults to APP-web)
    -p, --port=<port>        listen port
    --sticky                 cookie-based sticky routing
    --leader                 leader-only routing

Examples:

    $ flynn-host route:add http --app admin example.com/admin
    $ flynn-host route:add http --app www www.example.com
`)
}

func runHostRouteAdd(args *docopt.Args) error {
	if !args.Bool["http"] {
		return fmt.Errorf("only http routes are supported")
	}
	client, err := controllerClient()
	if err != nil {
		return err
	}
	app := args.String["--app"]
	service := args.String["--service"]
	if service == "" {
		service = app + "-web"
	}
	port := 0
	if args.String["--port"] != "" {
		port, err = strconv.Atoi(args.String["--port"])
		if err != nil {
			return err
		}
	}
	u, err := url.Parse("http://" + args.String["<domain>"])
	if err != nil {
		return fmt.Errorf("failed to parse %s as URL", args.String["<domain>"])
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	hr := &router.HTTPRoute{
		Service:       service,
		Domain:        u.Host,
		Port:          port,
		Sticky:        args.Bool["--sticky"],
		Leader:        args.Bool["--leader"],
		Path:          path,
		DrainBackends: true,
	}
	route := hr.ToRoute()
	if err := client.CreateRoute(app, route); err != nil {
		return err
	}
	fmt.Println(route.FormattedID())
	return nil
}
