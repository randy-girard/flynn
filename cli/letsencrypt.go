package main

import (
	"fmt"
	"log"
	"strings"

	cfg "github.com/flynn/flynn/cli/config"
	controller "github.com/flynn/flynn/controller/client"
	"github.com/flynn/go-docopt"
)

func init() {
	register("letsencrypt", runLetsEncrypt, `
usage: flynn letsencrypt enable <route-id>
       flynn letsencrypt disable <route-id>

Manage Let's Encrypt TLS certificates for routes.

Before using this command, ACME must be configured and enabled at the cluster
level using 'flynn-host acme configure' and 'flynn-host acme enable'.

Commands:
    enable   Enable automatic TLS certificate provisioning for a route
    disable  Disable automatic TLS certificate provisioning for a route

Arguments:
    <route-id>  The route ID (e.g., http/abc123) to enable/disable Let's Encrypt for

Examples:
    $ flynn letsencrypt enable http/abc123
    $ flynn letsencrypt disable http/abc123
`)
}

func runLetsEncrypt(args *docopt.Args, client controller.Client) error {
	if args.Bool["enable"] {
		return runLetsEncryptEnable(args, client)
	} else if args.Bool["disable"] {
		return runLetsEncryptDisable(args, client)
	}
	return fmt.Errorf("unknown command")
}

func runLetsEncryptEnable(args *docopt.Args, client controller.Client) error {
	routeID := args.String["<route-id>"]
	parts := strings.SplitN(routeID, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid route ID format, expected type/id (e.g., http/abc123)")
	}
	routeType := parts[0]
	if routeType != "http" {
		return fmt.Errorf("Let's Encrypt is only supported for HTTP routes")
	}

	// Check if ACME is enabled
	acmeConfig, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error checking ACME configuration: %s", err)
	}
	if !acmeConfig.Enabled {
		return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme configure --email=<email> --agree-tos' and 'flynn-host acme enable' first.")
	}

	// Get the route
	appName := mustApp()
	route, err := client.GetRoute(appName, routeID)
	if err != nil {
		return fmt.Errorf("error getting route: %s", err)
	}

	httpRoute := route.HTTPRoute()
	if httpRoute == nil {
		return fmt.Errorf("route is not an HTTP route")
	}

	// Set managed certificate domain to enable auto TLS
	domain := httpRoute.Domain
	route.ManagedCertificateDomain = &domain
	route.Certificate = nil
	route.LegacyTLSCert = ""
	route.LegacyTLSKey = ""
	if err := client.UpdateRoute(appName, routeID, route); err != nil {
		return fmt.Errorf("error updating route: %s", err)
	}

	fmt.Printf("Let's Encrypt enabled for route %s\n", routeID)
	fmt.Printf("A TLS certificate will be automatically provisioned for %s\n", httpRoute.Domain)

	// If this is the controller app, automatically clear the TLS pin
	// since the certificate will change to a Let's Encrypt certificate
	if appName == "controller" {
		if err := clearTLSPinForController(); err != nil {
			// Log warning but don't fail the command
			log.Printf("Warning: Could not automatically clear TLS pin: %s", err)
			fmt.Println("\nIMPORTANT: The controller's TLS certificate will change.")
			fmt.Println("You may need to run 'flynn cluster update-pin --clear' to avoid TLS pin mismatch errors.")
		} else {
			fmt.Println("\nThe TLS pin has been automatically cleared from your ~/.flynnrc")
			fmt.Println("since Let's Encrypt certificates are CA-signed and don't require pinning.")
		}
	}

	return nil
}

// clearTLSPinForController clears the TLS pin for the current cluster
// This is called when Let's Encrypt is enabled on the controller to avoid
// TLS pin mismatch errors after the certificate changes.
func clearTLSPinForController() error {
	cluster, err := getCluster()
	if err != nil {
		return err
	}

	if cluster.TLSPin == "" {
		// Already no pin, nothing to do
		return nil
	}

	cluster.TLSPin = ""
	if err := config.SaveTo(cfg.DefaultPath()); err != nil {
		return fmt.Errorf("error saving config: %s", err)
	}

	return nil
}

func runLetsEncryptDisable(args *docopt.Args, client controller.Client) error {
	routeID := args.String["<route-id>"]
	parts := strings.SplitN(routeID, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid route ID format, expected type/id (e.g., http/abc123)")
	}
	routeType := parts[0]
	if routeType != "http" {
		return fmt.Errorf("Let's Encrypt is only supported for HTTP routes")
	}

	// Get the route
	route, err := client.GetRoute(mustApp(), routeID)
	if err != nil {
		return fmt.Errorf("error getting route: %s", err)
	}

	httpRoute := route.HTTPRoute()
	if httpRoute == nil {
		return fmt.Errorf("route is not an HTTP route")
	}

	// Disable managed certificate and remove the certificate from the route
	route.ManagedCertificateDomain = nil
	route.Certificate = nil
	route.LegacyTLSCert = ""
	route.LegacyTLSKey = ""

	if err := client.UpdateRoute(mustApp(), routeID, route); err != nil {
		return fmt.Errorf("error updating route: %s", err)
	}

	fmt.Printf("Let's Encrypt disabled for route %s\n", routeID)
	fmt.Println("The route will no longer have automatic TLS certificate provisioning.")
	fmt.Println("The TLS certificate has been removed from the route.")
	return nil
}
