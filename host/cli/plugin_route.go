package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func runPluginRoute(args *docopt.Args) error {
	client, app, err := pluginRouteApp(args)
	if err != nil {
		return err
	}
	rt := plugin.NewPluginRouter(client, os.Stdout)
	switch {
	case args.Bool["add"] && args.Bool["http"]:
		return runPluginRouteAddHTTP(args, app, rt)
	case args.Bool["add"] && args.Bool["tcp"]:
		return runPluginRouteAddTCP(args, app, rt)
	case args.Bool["update"]:
		return runPluginRouteUpdate(args, app, rt)
	case args.Bool["remove"]:
		if err := rt.Remove(app, args.String["<id>"]); err != nil {
			return err
		}
		fmt.Printf("Route %s removed.\n", args.String["<id>"])
		return nil
	}
	return printPluginRoutes(app.ID, rt)
}

func pluginRouteApp(args *docopt.Args) (controller.Client, *ct.App, error) {
	client, err := controllerClient()
	if err != nil {
		return nil, nil, err
	}
	apps, err := client.AppList()
	if err != nil {
		return nil, nil, err
	}
	app, err := plugin.LookupPluginApp(apps, args.String["<plugin>"])
	if err != nil {
		return nil, nil, err
	}
	return client, app, nil
}

func runPluginRouteAddHTTP(args *docopt.Args, app *ct.App, rt *plugin.PluginRouter) error {
	cert, key, err := pluginRouteTLSCert(args)
	if err != nil {
		return err
	}
	port, err := pluginRoutePort(args)
	if err != nil {
		return err
	}
	route, err := rt.AddHTTP(app, plugin.HTTPRouteOptions{
		Domain:            args.String["<domain>"],
		Service:           args.String["--service"],
		Port:              port,
		AutoTLS:           args.Bool["--auto-tls"],
		TLSCert:           cert,
		TLSKey:            key,
		Sticky:            args.Bool["--sticky"],
		Leader:            args.Bool["--leader"],
		DrainBackends:     !args.Bool["--no-drain-backends"],
		DisableKeepAlives: args.Bool["--disable-keep-alives"],
	})
	if err != nil {
		return err
	}
	fmt.Println(route.FormattedID())
	return nil
}

func runPluginRouteAddTCP(args *docopt.Args, app *ct.App, rt *plugin.PluginRouter) error {
	port, err := pluginRoutePort(args)
	if err != nil {
		return err
	}
	cert, key, err := pluginRouteTLSCert(args)
	if err != nil {
		return err
	}
	route, err := rt.AddTCP(app, plugin.TCPRouteOptions{
		Service:       args.String["--service"],
		Port:          port,
		Leader:        args.Bool["--leader"],
		DrainBackends: !args.Bool["--no-drain-backends"],
		Domain:        args.String["--domain"],
		TLSMode:       args.String["--tls-mode"],
		AutoTLS:       args.Bool["--auto-tls"],
		TLSCert:       cert,
		TLSKey:        key,
	})
	if err != nil {
		return err
	}
	hr := route.TCPRoute()
	fmt.Printf("%s listening on port %d\n", hr.FormattedID(), hr.Port)
	if hr.Domain != "" {
		fmt.Printf("hostname %s tls_mode=%s\n", hr.Domain, hr.TLSMode)
	}
	fmt.Printf("On each host run: sudo flynn-host firewall:expose %d\n", hr.Port)
	return nil
}

func runPluginRouteUpdate(args *docopt.Args, app *ct.App, rt *plugin.PluginRouter) error {
	id := args.String["<id>"]
	typ := strings.Split(id, "/")[0]
	if typ == "tcp" {
		upd := plugin.TCPRouteUpdate{Service: args.String["--service"]}
		if args.Bool["--leader"] {
			upd.SetLeader = true
			upd.Leader = true
		} else if args.Bool["--no-leader"] {
			upd.SetLeader = true
		}
		route, err := rt.UpdateTCP(app, id, upd)
		if err != nil {
			return err
		}
		hr := route.TCPRoute()
		fmt.Printf("%s listening on port %d\n", hr.FormattedID(), hr.Port)
		return nil
	}
	cert, key, err := pluginRouteTLSCert(args)
	if err != nil {
		return err
	}
	upd := plugin.HTTPRouteUpdate{
		Service:   args.String["--service"],
		AutoTLS:   args.Bool["--auto-tls"],
		NoAutoTLS: args.Bool["--no-auto-tls"],
		TLSCert:   cert,
		TLSKey:    key,
	}
	if args.Bool["--sticky"] {
		upd.SetSticky = true
		upd.Sticky = true
	} else if args.Bool["--no-sticky"] {
		upd.SetSticky = true
	}
	if args.Bool["--leader"] {
		upd.SetLeader = true
		upd.Leader = true
	} else if args.Bool["--no-leader"] {
		upd.SetLeader = true
	}
	if args.Bool["--disable-keep-alives"] {
		upd.SetKeepAlives = true
		upd.DisableKeepAlives = true
	} else if args.Bool["--enable-keep-alives"] {
		upd.SetKeepAlives = true
	}
	route, err := rt.UpdateHTTP(app, id, upd)
	if err != nil {
		return err
	}
	fmt.Printf("updated %s\n", route.FormattedID())
	return nil
}

func printPluginRoutes(appID string, rt *plugin.PluginRouter) error {
	routes, err := rt.List(appID)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	defer w.Flush()
	listRec(w, "ROUTE", "SERVICE", "ID", "STICKY", "LEADER", "TLS", "PATH")
	for _, k := range routes {
		if k == nil {
			continue
		}
		port := strconv.Itoa(int(k.Port))
		tlsStatus := ""
		sticky := ""
		path := ""
		var rec, service string
		switch k.Type {
		case "tcp":
			service = k.Service
			rec = "tcp:" + port
		case "http":
			service = k.Service
			host := k.Domain
			if port != "0" {
				host = k.Domain + ":" + port
			}
			httpRoute := k.HTTPRoute()
			proto := "http"
			tlsStatus = "none"
			if k.ManagedCertificateDomain != nil && *k.ManagedCertificateDomain != "" {
				proto = "https"
				tlsStatus = "auto"
			} else if httpRoute.Certificate != nil || httpRoute.LegacyTLSCert != "" {
				proto = "https"
				tlsStatus = "manual"
			}
			sticky = fmt.Sprintf("%t", k.Sticky)
			path = httpRoute.Path
			rec = proto + ":" + host
		default:
			continue
		}
		listRec(w, rec, service, k.FormattedID(), sticky, k.Leader, tlsStatus, path)
	}
	return nil
}

func pluginRoutePort(args *docopt.Args) (int, error) {
	if args.String["--port"] == "" {
		return 0, nil
	}
	p, err := strconv.Atoi(args.String["--port"])
	if err != nil {
		return 0, fmt.Errorf("invalid --port: %s", args.String["--port"])
	}
	return p, nil
}

func pluginRouteTLSCert(args *docopt.Args) (string, string, error) {
	certPath := args.String["--tls-cert"]
	keyPath := args.String["--tls-key"]
	if certPath == "" && keyPath == "" {
		return "", "", nil
	}
	if certPath == "" || keyPath == "" {
		return "", "", fmt.Errorf("both --tls-cert and --tls-key are required")
	}
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return "", "", fmt.Errorf("read TLS cert: %w", err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return "", "", fmt.Errorf("read TLS key: %w", err)
	}
	return string(cert), string(key), nil
}
