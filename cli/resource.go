package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/pgappliance"
	"github.com/randy-girard/flynn/pkg/resourceexpose"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	register("resource", runResourceList, `
usage: flynn resource

List resources for the app.
`)
	register("resource:add", runResourceAdd, `
usage: flynn resource:add <provider> [--as <name>] [--follow <resource>] [--runtime <name>] [--replication <mode>]

Provision a new resource for the app using <provider>.

For postgres, the installed plugin (provider postgres at postgres-plugin.discoverd)
receives --as, --follow, --runtime, and --replication. The platform appliance
at postgres-api.discoverd is not used. --as ANALYTICS sets only ANALYTICS_URL.
The default name DATABASE sets only DATABASE_URL.

Options:
	--as=<name>              attachment env name (default DATABASE)
	--follow=<resource>      read-only follower of an existing postgres resource
	--runtime=<name>         runtime name for this instance
	--replication=<mode>     streaming (same major) or logical (major upgrade)
`)
	register("resource:attach", runResourceAttach, `
usage: flynn resource:attach <provider> <resource> [--as <name>]

Attach an existing resource to this app.

For postgres, --as sets one env var (<NAME>_URL). The default name is the
resource's existing *_URL key. The same resource can attach to several apps
with different names.

Options:
	--as=<name>  attachment env name
`)
	register("resource:detach", runResourceDetach, `
usage: flynn resource:detach <provider> <resource>

Detach a resource from this app and remove the attachment env var.
`)
	register("resource:remove", runResourceRemove, `
usage: flynn resource:remove <provider> [<resource>]

Remove the existing <resource> provided by <provider>. Resolves <resource> automatically if unambiguous.
`)
	register("resource:expose", runResourceExpose, `
usage: flynn resource:expose <provider> [--domain <host>] [-p <port>] [--tls-mode <mode>] [--auto-tls] [-c <tls-cert> -k <tls-key>]

Export a datastore (postgres, mysql, mongodb, redis, kafka, clickhouse)
on a TCP route with a stable hostname, then open the host port with
flynn-host firewall:expose.

Options:
	--domain=<host>        hostname (default: SERVICE.CLUSTERDOMAIN)
	-p, --port=<port>      TCP port (assigned from 3000-3500 if omitted)
	--tls-mode=<mode>      off, passthrough (default), or terminate
	--auto-tls             Let's Encrypt on a terminate route (requires ACME)
	-c, --tls-cert=<tls-cert>  PEM cert for tls_mode=terminate, - for stdin
	-k, --tls-key=<tls-key>    PEM key for tls_mode=terminate, - for stdin

passthrough is the default so postgres/mysql native SSL still works. Use
terminate for TLS-first protocols (Redis TLS, Kafka, MongoDB, ClickHouse)
when the backend is plaintext. flynn-host also opens TCP route ports on
its 15s firewall sync; pin the port on every node:

    sudo flynn-host firewall:expose PORT
`)
	register("resource:unexpose", runResourceUnexpose, `
usage: flynn resource:unexpose <provider> [--domain <host>]

Remove the TCP export route for <provider> and print the matching
flynn-host firewall:unexpose command.
`)
}

func runResourceList(args *docopt.Args, client controller.Client) error {

	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	var provider *ct.Provider

	listRec(w, "ID", "Provider ID", "Provider Name")
	for _, j := range resources {
		provider, err = client.GetProvider(j.ProviderID)
		if err != nil {
			return err
		}
		listRec(w, j.ID, j.ProviderID, provider.Name)
	}

	return err
}

func runResourceAdd(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	if err := rejectPlatformPostgresAdd(provider, client); err != nil {
		return err
	}

	req := &ct.ResourceReq{ProviderID: provider, Apps: []string{mustApp()}}
	cfg, err := postgresProvisionConfig(provider, args.String["--as"], args.String["--follow"], args.String["--runtime"], args.String["--replication"])
	if err != nil {
		return err
	}
	req.Config = cfg
	res, err := client.ProvisionResource(req)
	if err != nil {
		return err
	}

	env := make(map[string]*string)
	for k, v := range res.Env {
		s := v
		env[k] = &s
	}

	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}

	log.Printf("Created resource %s and release %s.", res.ID, releaseID)

	return nil
}

// rejectPlatformPostgresAdd stops `flynn resource:add postgres` from creating
// a role on the built-in appliance. When the postgres plugin is installed it
// registers provider postgres at postgres-plugin.discoverd, and that URL is allowed.
func rejectPlatformPostgresAdd(provider string, client controller.Client) error {
	if provider != "postgres" {
		return nil
	}
	p, err := client.GetProvider(provider)
	if err != nil {
		if errors.Is(err, controller.ErrNotFound) {
			return pgappliance.ErrTenantProvision
		}
		return err
	}
	if p == nil || pgappliance.IsPlatformApplianceURL(p.URL) {
		return pgappliance.ErrTenantProvision
	}
	return nil
}

// postgresProvisionConfig is the body forwarded to flynn-plugin-postgres.
// Other providers are unchanged. Empty postgres flags send no config.
func postgresProvisionConfig(provider, as, follow, runtime, replication string) (*json.RawMessage, error) {
	if provider != "postgres" {
		return nil, nil
	}
	cfg := map[string]string{}
	if as != "" {
		cfg["as"] = as
	}
	if follow != "" {
		cfg["follow"] = follow
	}
	if runtime != "" {
		cfg["runtime"] = runtime
	}
	if replication != "" {
		cfg["replication"] = replication
	}
	if len(cfg) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	msg := json.RawMessage(raw)
	return &msg, nil
}

func singleAttachmentEnv(resourceEnv map[string]string, as string) (map[string]string, error) {
	var key, val string
	n := 0
	for k, v := range resourceEnv {
		if strings.HasSuffix(k, "_URL") && v != "" {
			n++
			key, val = k, v
		}
	}
	if n != 1 {
		return nil, fmt.Errorf("postgres attachment expects exactly one *_URL, found %d", n)
	}
	name := key[:len(key)-len("_URL")]
	if strings.TrimSpace(as) != "" {
		name = strings.ToUpper(strings.TrimSpace(as))
	}
	return map[string]string{name + "_URL": val}, nil
}

func runResourceAttach(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	resource := args.String["<resource>"]
	res, err := client.AddResourceApp(provider, resource, mustApp())
	if err != nil {
		return err
	}
	env := make(map[string]*string)
	if provider == "postgres" {
		one, err := singleAttachmentEnv(res.Env, args.String["--as"])
		if err != nil {
			return err
		}
		for k, v := range one {
			s := v
			env[k] = &s
		}
	} else {
		for k, v := range res.Env {
			s := v
			env[k] = &s
		}
	}
	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}
	log.Printf("Attached resource %s and release %s.", res.ID, releaseID)
	return nil
}

func runResourceDetach(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	resource := args.String["<resource>"]
	res, err := client.DeleteResourceApp(provider, resource, mustApp())
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	env := make(map[string]*string)
	for k, v := range res.Env {
		if release.Env[k] == v {
			env[k] = nil
		}
	}
	if provider == "postgres" {
		for k, v := range release.Env {
			if !strings.HasSuffix(k, "_URL") {
				continue
			}
			for _, rv := range res.Env {
				if v == rv {
					env[k] = nil
				}
			}
		}
	}
	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}
	log.Printf("Detached resource %s and release %s.", res.ID, releaseID)
	return nil
}

func runResourceRemove(args *docopt.Args, client controller.Client) error {
	provider := args.String["<provider>"]
	resource := args.String["<resource>"]

	var err error
	if resource == "" {
		resource, err = resolveResource(provider, client)
		if err != nil {
			return err
		}
	}

	res, err := client.DeleteResource(provider, resource)
	if err != nil {
		return err
	}

	release, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}

	// Unset all the keys associated with this resource
	env := make(map[string]*string)
	for k := range res.Env {
		// Only unset the key if it hasn't been modified
		if release.Env[k] == res.Env[k] {
			env[k] = nil
		}
	}

	releaseID, err := setEnv(client, "", env)
	if err != nil {
		return err
	}

	log.Printf("Deleted resource %s, created release %s.", res.ID, releaseID)

	return nil
}

func resolveResource(provider string, client controller.Client) (string, error) {
	resources, err := client.AppResourceList(mustApp())
	if err != nil {
		return "", err
	}
	var matched []*ct.Resource
	for _, r := range resources {
		p, err := client.GetProvider(r.ProviderID)
		if err != nil {
			return "", err
		}
		if r.ProviderID == provider || p.Name == provider {
			matched = append(matched, r)
		}
	}
	if len(matched) != 1 {
		return "", fmt.Errorf("App has more than one resource for %s, specify resource ID", provider)
	}
	return matched[0].ID, nil
}

func runResourceExpose(args *docopt.Args, client controller.Client) error {
	spec, err := resourceexpose.Lookup(args.String["<provider>"])
	if err != nil {
		return err
	}
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	appliance, service := spec.ResolveAppService(appRelease.Env)
	if appliance == "" {
		return fmt.Errorf("no %s resource on this app; provision with flynn resource:add %s", spec.Provider, spec.Provider)
	}

	domain := strings.TrimSpace(args.String["--domain"])
	if domain == "" {
		clusterDomain := clusterRouteDomain(client)
		domain = resourceexpose.DefaultHostname(service, clusterDomain)
	}

	port := 0
	if args.String["--port"] != "" {
		port, err = strconv.Atoi(args.String["--port"])
		if err != nil {
			return err
		}
	}

	autoTLS := args.Bool["--auto-tls"]
	tlsCert, tlsKey, err := parseTLSCert(args)
	if err != nil {
		return err
	}
	if autoTLS && (tlsCert != "" || tlsKey != "") {
		return errors.New("--auto-tls cannot be used with --tls-cert or --tls-key")
	}

	tlsMode := args.String["--tls-mode"]
	route, err := resourceexpose.NewTCPRoute(service, domain, tlsMode, port, spec.Leader, autoTLS)
	if err != nil {
		return err
	}
	if tlsCert != "" || tlsKey != "" {
		route.LegacyTLSCert = tlsCert
		route.LegacyTLSKey = tlsKey
		if route.TLSMode == "" || route.TLSMode == router.TLSModePassthrough {
			route.TLSMode = router.TLSModeTerminate
		}
	}

	if autoTLS {
		acmeConfig, err := client.GetACMEConfig()
		if err != nil {
			return fmt.Errorf("error checking ACME configuration: %s", err)
		}
		if !acmeConfig.Enabled {
			return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host acme:configure --email=<email> --agree-tos' and 'flynn-host acme:enable' first.")
		}
	}

	existing, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}
	if prev := resourceexpose.FindTCPRoute(existing, service, domain); prev != nil {
		fmt.Printf("%s already exposed as %s on port %d (%s)\n", spec.Provider, prev.Domain, prev.Port, prev.FormattedID())
		fmt.Printf("On each host: %s\n", resourceexpose.FirewallExposeCommand(int(prev.Port)))
		return nil
	}

	if err := client.CreateRoute(mustApp(), route); err != nil {
		return err
	}
	fmt.Printf("%s listening on port %d\n", route.FormattedID(), route.Port)
	if route.Domain != "" {
		fmt.Printf("hostname %s tls_mode=%s\n", route.Domain, displayTLSMode(route.TLSMode))
	}
	fmt.Printf("On each host run: %s\n", resourceexpose.FirewallExposeCommand(int(route.Port)))
	fmt.Printf("flynn-host also opens TCP route ports on its firewall sync.\n")
	return nil
}

func runResourceUnexpose(args *docopt.Args, client controller.Client) error {
	spec, err := resourceexpose.Lookup(args.String["<provider>"])
	if err != nil {
		return err
	}
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return err
	}
	_, service := spec.ResolveAppService(appRelease.Env)
	if service == "" {
		return fmt.Errorf("no %s resource on this app", spec.Provider)
	}
	routes, err := client.AppRouteList(mustApp())
	if err != nil {
		return err
	}
	route := resourceexpose.FindTCPRoute(routes, service, strings.TrimSpace(args.String["--domain"]))
	if route == nil {
		return fmt.Errorf("no TCP export route for %s", spec.Provider)
	}
	if err := client.DeleteRoute(mustApp(), route.FormattedID()); err != nil {
		return err
	}
	fmt.Printf("Route %s removed.\n", route.FormattedID())
	fmt.Printf("On each host run: %s\n", resourceexpose.FirewallUnexposeCommand(int(route.Port)))
	return nil
}

func clusterRouteDomain(client controller.Client) string {
	var explicit string
	if rel, err := client.GetAppRelease("controller"); err == nil && rel != nil {
		explicit = rel.Env["DEFAULT_ROUTE_DOMAIN"]
	}
	var controllerURL string
	if cluster, err := getCluster(); err == nil && cluster != nil {
		controllerURL = cluster.ControllerURL
	}
	return resourceexpose.ClusterDomain(controllerURL, explicit)
}

func displayTLSMode(mode string) string {
	if mode == "" {
		return "off"
	}
	return mode
}
