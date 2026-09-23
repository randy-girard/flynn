package main

import (
	"fmt"
	"strings"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	plugin.CorePluginCommands = append(plugin.CorePluginCommands, "letsencrypt")
	register("letsencrypt", runLetsEncryptStatus, `
usage: flynn letsencrypt
       flynn letsencrypt:status [<hostname-or-route-id>]

Show Let's Encrypt status for a hostname, or cluster ACME config with no argument.
`)
	register("letsencrypt:enable", runLetsEncryptEnable, `
usage: flynn letsencrypt:enable <hostname-or-route-id>

Enable Let's Encrypt HTTPS on an HTTP route (hostname or http/<id>).

Examples:

    $ flynn route:add http www.example.com
    $ flynn letsencrypt:enable www.example.com
`)
	register("letsencrypt:disable", runLetsEncryptDisable, `
usage: flynn letsencrypt:disable <hostname-or-route-id>

Disable Let's Encrypt HTTPS on an HTTP route. HTTP still works.

Examples:

    $ flynn letsencrypt:disable www.example.com
`)
	register("letsencrypt:status", runLetsEncryptStatus, `
usage: flynn letsencrypt:status [<hostname-or-route-id>]

Show Let's Encrypt status for a hostname, or cluster ACME config with no argument.
`)
}

func runLetsEncryptEnable(args *docopt.Args, client controller.Client) error {
	return toggleLetsEncrypt(client, args.String["<hostname-or-route-id>"], true)
}

func runLetsEncryptDisable(args *docopt.Args, client controller.Client) error {
	return toggleLetsEncrypt(client, args.String["<hostname-or-route-id>"], false)
}

func runLetsEncryptStatus(args *docopt.Args, client controller.Client) error {
	if err := requireLetsEncryptPlugin(client); err != nil {
		return err
	}
	target := strings.TrimSpace(args.String["<hostname-or-route-id>"])
	if target != "" {
		rt, app, err := findHTTPRoute(client, target)
		if err != nil {
			return err
		}
		on := rt.ManagedCertificateDomain != nil && *rt.ManagedCertificateDomain != ""
		state := "off"
		if on {
			state = "on"
		}
		fmt.Printf("HTTPS %s for %s (app %s, %s/%s)\n", state, rt.Domain, app.Name, rt.Type, rt.ID)
		return nil
	}
	cfg, err := client.GetACMEConfig()
	if err != nil {
		return err
	}
	fmt.Printf("cluster enabled: %t\n", cfg.Enabled)
	if cfg.ContactEmail != "" {
		fmt.Printf("contact: %s\n", cfg.ContactEmail)
	}
	return nil
}

func toggleLetsEncrypt(client controller.Client, target string, enable bool) error {
	if err := requireLetsEncryptPlugin(client); err != nil {
		return err
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("hostname or route id is required")
	}
	if enable {
		cfg, err := client.GetACMEConfig()
		if err != nil {
			return err
		}
		if cfg == nil || !cfg.Enabled {
			return fmt.Errorf("Let's Encrypt is not enabled for this cluster. Run: sudo flynn-host letsencrypt:configure --email=<email> --agree-tos")
		}
	}
	rt, app, err := findHTTPRoute(client, target)
	if err != nil {
		return err
	}
	domain := rt.Domain
	already := rt.ManagedCertificateDomain != nil && *rt.ManagedCertificateDomain != ""
	if enable && already {
		fmt.Printf("HTTPS already enabled for %s\n", domain)
		return nil
	}
	if !enable && !already {
		fmt.Printf("HTTPS already off for %s\n", domain)
		return nil
	}
	if enable {
		rt.ManagedCertificateDomain = &domain
		rt.Certificate = nil
		rt.LegacyTLSCert = ""
		rt.LegacyTLSKey = ""
	} else {
		rt.ManagedCertificateDomain = nil
	}
	routeID := fmt.Sprintf("%s/%s", rt.Type, rt.ID)
	if err := client.UpdateRoute(app.Name, routeID, rt); err != nil {
		return err
	}
	if enable {
		fmt.Printf("HTTPS enabled for %s\n", domain)
	} else {
		fmt.Printf("HTTPS disabled for %s\n", domain)
	}
	return nil
}

func requireLetsEncryptPlugin(client controller.Client) error {
	apps, err := client.AppList()
	if err != nil {
		return err
	}
	if !letsEncryptPluginInstalled(apps) {
		return fmt.Errorf("Let's Encrypt plugin is not installed. Run: sudo flynn-host plugin:install letsencrypt")
	}
	return nil
}

func letsEncryptPluginInstalled(apps []*ct.App) bool {
	for _, a := range apps {
		if a == nil {
			continue
		}
		if a.Name == "letsencrypt" && a.Plugin() {
			return true
		}
		if a.Name == "acme" && a.System() {
			return true
		}
	}
	return false
}

func findHTTPRoute(client controller.Client, target string) (*router.Route, *ct.App, error) {
	apps, err := client.AppList()
	if err != nil {
		return nil, nil, err
	}
	appByID := make(map[string]*ct.App, len(apps))
	for _, a := range apps {
		appByID[a.ID] = a
	}
	routes, err := client.RouteList()
	if err != nil {
		return nil, nil, err
	}
	var matches []*router.Route
	for _, rt := range routes {
		if rt.Type != "http" {
			continue
		}
		id := fmt.Sprintf("%s/%s", rt.Type, rt.ID)
		if strings.EqualFold(rt.Domain, target) || rt.ID == target || id == target {
			matches = append(matches, rt)
		}
	}
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("HTTP route not found for %s", target)
	}
	if len(matches) > 1 && !strings.Contains(target, "/") {
		var names []string
		for _, rt := range matches {
			appName := rt.ParentRef
			if strings.HasPrefix(rt.ParentRef, ct.RouteParentRefPrefix) {
				if app := appByID[strings.TrimPrefix(rt.ParentRef, ct.RouteParentRefPrefix)]; app != nil {
					appName = app.Name
				}
			}
			names = append(names, fmt.Sprintf("%s %s/%s", appName, rt.Type, rt.ID))
		}
		return nil, nil, fmt.Errorf("several HTTP routes share %s: %s", target, strings.Join(names, "; "))
	}
	rt := matches[0]
	if !strings.HasPrefix(rt.ParentRef, ct.RouteParentRefPrefix) {
		return nil, nil, fmt.Errorf("route %s has no parent app", rt.ID)
	}
	app := appByID[strings.TrimPrefix(rt.ParentRef, ct.RouteParentRefPrefix)]
	if app == nil {
		return nil, nil, fmt.Errorf("app for route %s not found", rt.ID)
	}
	return rt, app, nil
}
