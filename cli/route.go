package main

import (
	"bytes"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	register("route", runRouteList, `
usage: flynn route

List routes for the application.
`)
	register("route:add", runRouteAdd, `
usage: flynn route:add http [-s <service>] [-p <port>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--sticky] [--leader] [--no-leader] [--no-drain-backends] [--disable-keep-alives] <domain>
       flynn route:add tcp [-s <service>] [-p <port>] [--domain <host>] [--tls-mode <mode>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--leader] [--no-drain-backends]

Add a route to an application.

Options:
	-s, --service=<service>    service name to route domain to (defaults to APPNAME-web)
	-c, --tls-cert=<tls-cert>  path to PEM encoded certificate for TLS, - for stdin
	-k, --tls-key=<tls-key>    path to PEM encoded private key for TLS, - for stdin
	--auto-tls                 automatically provision TLS certificate via Let's Encrypt
	--tls-mode=<mode>          TCP TLS: off, passthrough, or terminate
	--domain=<host>            hostname stored on TCP routes (for TLS identity / DNS)
	--sticky                   enable cookie-based sticky routing (http only)
	--leader                   enable leader-only routing mode
	-p, --port=<port>          port to accept traffic on
	--no-drain-backends        don't wait for in-flight requests to complete before stopping backends
	--disable-keep-alives      disable keep-alives between the router and backends for the given route

Examples:

	$ flynn route:add http example.com

	$ flynn route:add http --auto-tls example.com

	$ flynn route:add tcp

	$ flynn route:add tcp --service postgres --leader --domain postgres.example.com --tls-mode passthrough
`)
	register("route:update", runRouteUpdate, `
usage: flynn route:update <id> [-s <service>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--no-auto-tls] [--sticky] [--no-sticky] [--leader] [--no-leader] [--disable-keep-alives] [--enable-keep-alives]

Update a route.

Options:
	-s, --service=<service>    service name to route domain to
	-c, --tls-cert=<tls-cert>  path to PEM encoded certificate for TLS, - for stdin (http only)
	-k, --tls-key=<tls-key>    path to PEM encoded private key for TLS, - for stdin (http only)
	--auto-tls                 automatically provision TLS certificate via Let's Encrypt (http only)
	--no-auto-tls              disable automatic TLS certificate provisioning
	--sticky                   enable cookie-based sticky routing (http only)
	--no-sticky                disable cookie-based sticky routing
	--leader                   enable leader-only routing mode
	--no-leader                disable leader-only routing mode
	--disable-keep-alives      disable keep-alives between the router and backends
	--enable-keep-alives       enable keep-alives between the router and backends
`)
	register("route:remove", runRouteRemove, `
usage: flynn route:remove <id>

Remove a route.
`)
}

func runRouteAdd(args *docopt.Args, client controller.Client) error {
	switch {
	case args.Bool["http"]:
		return runRouteAddHTTP(args, client)
	case args.Bool["tcp"]:
		return runRouteAddTCP(args, client)
	default:
		return fmt.Errorf("Route type %s not supported.", args.String["-t"])
	}
}

func runRouteUpdate(args *docopt.Args, client controller.Client) error {
	typ := strings.Split(args.String["<id>"], "/")[0]
	switch typ {
	case "http":
		return runRouteUpdateHTTP(args, client)
	case "tcp":
		return runRouteUpdateTCP(args, client)
	default:
		return fmt.Errorf("Route type %s not supported.", typ)
	}
}

func runRouteList(_ *docopt.Args, client controller.Client) error {

	routes, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	var route, port, protocol, service, sticky, path, tlsStatus string
	listRec(w, "ROUTE", "SERVICE", "ID", "STICKY", "LEADER", "TLS", "PATH")
	for _, k := range routes {
		port = strconv.Itoa(int(k.Port))
		tlsStatus = ""
		switch k.Type {
		case "tcp":
			route = port
			protocol = "tcp"
			service = k.TCPRoute().Service
			tcp := k.TCPRoute()
			switch router.NormalizeTLSMode(tcp.TLSMode) {
			case router.TLSModeTerminate:
				protocol = "tcp+tls"
				if k.ManagedCertificateDomain != nil && *k.ManagedCertificateDomain != "" {
					tlsStatus = "auto"
				} else if tcp.Certificate != nil || tcp.LegacyTLSCert != "" {
					tlsStatus = "manual"
				} else {
					tlsStatus = "terminate"
				}
			case router.TLSModePassthrough:
				tlsStatus = "passthrough"
			default:
				tlsStatus = "none"
			}
			if tcp.Domain != "" {
				route = tcp.Domain + ":" + port
			}
		case "http":
			route = k.HTTPRoute().Domain
			if port != "0" {
				route = k.HTTPRoute().Domain + ":" + port
			}
			service = k.TCPRoute().Service
			httpRoute := k.HTTPRoute()
			if k.ManagedCertificateDomain != nil && *k.ManagedCertificateDomain != "" {
				protocol = "https"
				tlsStatus = "auto"
			} else if httpRoute.Certificate != nil || httpRoute.LegacyTLSCert != "" {
				protocol = "https"
				tlsStatus = "manual"
			} else {
				protocol = "http"
				tlsStatus = "none"
			}
			sticky = fmt.Sprintf("%t", k.Sticky)
			path = k.HTTPRoute().Path
		}
		listRec(w, protocol+":"+route, service, k.FormattedID(), sticky, k.Leader, tlsStatus, path)
	}
	return nil
}

func runRouteAddTCP(args *docopt.Args, client controller.Client) error {
	service := args.String["--service"]
	if service == "" {
		service = mustApp() + "-web"
	}

	port := 0
	if args.String["--port"] != "" {
		p, err := strconv.Atoi(args.String["--port"])
		if err != nil {
			return err
		}
		port = p
	}

	tlsMode := router.NormalizeTLSMode(args.String["--tls-mode"])
	if args.String["--tls-mode"] != "" && !router.ValidTLSMode(tlsMode) {
		return fmt.Errorf("invalid --tls-mode %q (off, passthrough, terminate)", args.String["--tls-mode"])
	}

	autoTLS := args.Bool["--auto-tls"]
	tlsCert, tlsKey, err := parseTLSCert(args)
	if err != nil {
		return err
	}
	if autoTLS && (tlsCert != "" || tlsKey != "") {
		return errors.New("--auto-tls cannot be used with --tls-cert or --tls-key")
	}
	if autoTLS {
		tlsMode = router.TLSModeTerminate
		acmeConfig, err := client.GetACMEConfig()
		if err != nil {
			return fmt.Errorf("error checking ACME configuration: %s", err)
		}
		if !acmeConfig.Enabled {
			return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' and 'flynn-host acme:enable' first.")
		}
	} else if tlsCert != "" || tlsKey != "" {
		tlsMode = router.TLSModeTerminate
	}

	domain := strings.TrimSpace(args.String["--domain"])
	hr := &router.TCPRoute{
		Service:       service,
		Port:          port,
		Leader:        args.Bool["--leader"],
		DrainBackends: !args.Bool["--no-drain-backends"],
		Domain:        domain,
		TLSMode:       tlsMode,
		LegacyTLSCert: tlsCert,
		LegacyTLSKey:  tlsKey,
	}
	if autoTLS && domain != "" {
		hr.ManagedCertificateDomain = &domain
	}

	r := hr.ToRoute()
	if err := client.CreateRoute(mustApp(), r); err != nil {
		return err
	}
	hr = r.TCPRoute()
	fmt.Printf("%s listening on port %d\n", hr.FormattedID(), hr.Port)
	if hr.Domain != "" {
		fmt.Printf("hostname %s tls_mode=%s\n", hr.Domain, displayTLSMode(hr.TLSMode))
	}
	fmt.Printf("On each host run: sudo flynn-host firewall:expose %d\n", hr.Port)
	return nil
}

func runRouteAddHTTP(args *docopt.Args, client controller.Client) error {
	service := args.String["--service"]
	if service == "" {
		service = mustApp() + "-web"
	}

	autoTLS := args.Bool["--auto-tls"]
	tlsCert, tlsKey, err := parseTLSCert(args)
	if err != nil {
		return err
	}

	// Validate that --auto-tls and manual TLS cert are mutually exclusive
	if autoTLS && (tlsCert != "" || tlsKey != "") {
		return errors.New("--auto-tls cannot be used with --tls-cert or --tls-key")
	}

	// Check if ACME is enabled when auto-TLS is requested
	if autoTLS {
		acmeConfig, err := client.GetACMEConfig()
		if err != nil {
			return fmt.Errorf("error checking ACME configuration: %s", err)
		}
		if !acmeConfig.Enabled {
			return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' and 'flynn-host acme:enable' first.")
		}
	}

	port := 0
	if args.String["--port"] != "" {
		p, err := strconv.Atoi(args.String["--port"])
		if err != nil {
			return err
		}
		port = p
	}

	u, err := url.Parse("http://" + args.String["<domain>"])
	if err != nil {
		return fmt.Errorf("Failed to parse %s as URL", args.String["<domain>"])
	}
	if router.HTTPPathRequiresClusterAdmin(u.Path) {
		return fmt.Errorf("path-based HTTP routes (%s) can only be created with flynn-host route:add", args.String["<domain>"])
	}

	hr := &router.HTTPRoute{
		Service:           service,
		Domain:            u.Host,
		Port:              port,
		LegacyTLSCert:     tlsCert,
		LegacyTLSKey:      tlsKey,
		Sticky:            args.Bool["--sticky"],
		Leader:            args.Bool["--leader"],
		Path:              u.Path,
		DrainBackends:     !args.Bool["--no-drain-backends"],
		DisableKeepAlives: args.Bool["--disable-keep-alives"],
	}

	// Set managed certificate domain if auto-TLS is enabled
	if autoTLS {
		hr.ManagedCertificateDomain = &u.Host
	}

	route := hr.ToRoute()
	if err := client.CreateRoute(mustApp(), route); err != nil {
		return err
	}
	fmt.Println(route.FormattedID())
	return nil
}

func runRouteUpdateTCP(args *docopt.Args, client controller.Client) error {
	id := args.String["<id>"]
	appName := mustApp()

	route, err := client.GetRoute(appName, id)
	if err != nil {
		return err
	}

	service := args.String["--service"]
	if service == "" {
		return errors.New("No service name given")
	}
	route.Service = service

	if args.Bool["--leader"] {
		route.Leader = true
	} else if args.Bool["--no-leader"] {
		route.Leader = false
	}

	if err := client.UpdateRoute(appName, id, route); err != nil {
		return err
	}
	hr := route.TCPRoute()
	fmt.Printf("%s listening on port %d\n", hr.FormattedID(), hr.Port)
	return nil
}

func runRouteUpdateHTTP(args *docopt.Args, client controller.Client) error {
	id := args.String["<id>"]
	appName := mustApp()

	route, err := client.GetRoute(appName, id)
	if err != nil {
		return err
	}

	if service := args.String["--service"]; service != "" {
		route.Service = service
	}

	route.Certificate = nil
	route.LegacyTLSCert, route.LegacyTLSKey, err = parseTLSCert(args)
	if err != nil {
		return err
	}

	// Handle auto-TLS options
	if args.Bool["--auto-tls"] {
		// Validate that --auto-tls and manual TLS cert are mutually exclusive
		if route.LegacyTLSCert != "" || route.LegacyTLSKey != "" {
			return errors.New("--auto-tls cannot be used with --tls-cert or --tls-key")
		}
		// Check if ACME is enabled
		acmeConfig, err := client.GetACMEConfig()
		if err != nil {
			return fmt.Errorf("error checking ACME configuration: %s", err)
		}
		if !acmeConfig.Enabled {
			return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' and 'flynn-host acme:enable' first.")
		}
		route.ManagedCertificateDomain = &route.Domain
	} else if args.Bool["--no-auto-tls"] {
		route.ManagedCertificateDomain = nil
	}

	if args.Bool["--sticky"] {
		route.Sticky = true
	} else if args.Bool["--no-sticky"] {
		route.Sticky = false
	}

	if args.Bool["--leader"] {
		route.Leader = true
	} else if args.Bool["--no-leader"] {
		route.Leader = false
	}

	if args.Bool["--disable-keep-alives"] {
		route.DisableKeepAlives = true
	} else if args.Bool["--enable-keep-alives"] {
		route.DisableKeepAlives = false
	}

	if err := client.UpdateRoute(appName, id, route); err != nil {
		return err
	}
	fmt.Printf("updated %s\n", route.FormattedID())
	return nil
}

func parseTLSCert(args *docopt.Args) (string, string, error) {
	tlsCertPath := args.String["--tls-cert"]
	tlsKeyPath := args.String["--tls-key"]
	var tlsCert []byte
	var tlsKey []byte
	if tlsCertPath != "" && tlsKeyPath != "" {
		var stdin []byte

		if tlsCertPath == "-" || tlsKeyPath == "-" {
			var err error
			stdin, err = ioutil.ReadAll(os.Stdin)
			if err != nil {
				return "", "", fmt.Errorf("Failed to read from stdin: %s", err)
			}
		}

		var err error
		tlsCert, err = readPEM("CERTIFICATE", tlsCertPath, stdin)
		if err != nil {
			return "", "", fmt.Errorf("Failed to read TLS cert: %s", err)
		}
		tlsKey, err = readPEM("PRIVATE KEY", tlsKeyPath, stdin)
		if err != nil {
			return "", "", fmt.Errorf("Failed to read TLS key: %s", err)
		}
	} else if tlsCertPath != "" || tlsKeyPath != "" {
		return "", "", errors.New("Both the TLS certificate AND private key need to be specified")
	}
	return string(tlsCert), string(tlsKey), nil
}

func readPEM(typ string, path string, stdin []byte) ([]byte, error) {
	if path == "-" {
		var buf bytes.Buffer
		var block *pem.Block
		for {
			block, stdin = pem.Decode(stdin)
			if block == nil {
				break
			}
			if block.Type == typ {
				pem.Encode(&buf, block)
			}
		}
		if buf.Len() > 0 {
			return buf.Bytes(), nil
		}
		return nil, errors.New("No PEM blocks found in stdin")
	}
	return ioutil.ReadFile(path)
}

func runRouteRemove(args *docopt.Args, client controller.Client) error {
	routeID := args.String["<id>"]

	if err := client.DeleteRoute(mustApp(), routeID); err != nil {
		return err
	}
	fmt.Printf("Route %s removed.\n", routeID)
	return nil
}
