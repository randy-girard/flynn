package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	acmelib "github.com/eggsampler/acme/v3"
	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/plugin"
)

const (
	defaultACMEDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"
	stagingACMEDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

func init() {
	Register("acme", runACMEStatusCmd, `
usage: flynn-host acme

Show current ACME/Let's Encrypt configuration status.
`)
	Register("acme:configure", runACMEConfigureCmd, `
usage: flynn-host acme:configure --email=<email> [--agree-tos] [--staging] [--directory-url=<url>]

Configure ACME with a contact email address.

ACME must be configured and enabled before automatic TLS certificates can be
provisioned with flynn letsencrypt:enable. Requires the Let's Encrypt plugin.

Options:
    --email=<email>          Contact email for Let's Encrypt account (required)
    --agree-tos              Agree to the Let's Encrypt Terms of Service
    --staging                Use Let's Encrypt staging server (for testing, issues untrusted certs)
    --directory-url=<url>    ACME directory URL (defaults to Let's Encrypt production)

Examples:
    $ flynn-host letsencrypt:configure --email=admin@example.com --agree-tos
    $ flynn-host letsencrypt:configure --email=admin@example.com --agree-tos --staging
`)
	Register("letsencrypt:configure", runACMEConfigureCmd, `
usage: flynn-host letsencrypt:configure --email=<email> [--agree-tos] [--staging] [--directory-url=<url>]

Configure a Let's Encrypt / ACME account for this cluster.

The Let's Encrypt plugin must be installed first:
    sudo flynn-host plugin:install letsencrypt

Options:
    --email=<email>          Contact email for Let's Encrypt account (required)
    --agree-tos              Agree to the Let's Encrypt Terms of Service
    --staging                Use Let's Encrypt staging server (for testing, issues untrusted certs)
    --directory-url=<url>    ACME directory URL (defaults to Let's Encrypt production)

Examples:
    $ flynn-host letsencrypt:configure --email=admin@example.com --agree-tos
    $ flynn-host letsencrypt:configure --email=admin@example.com --agree-tos --staging
`)
	Register("letsencrypt:enable", runACMEEnableCmd, `
usage: flynn-host letsencrypt:enable

Enable Let's Encrypt for the cluster.
`)
	Register("letsencrypt:disable", runACMEDisableCmd, `
usage: flynn-host letsencrypt:disable

Disable Let's Encrypt for the cluster.
`)
	Register("letsencrypt:status", runACMEStatusCmd, `
usage: flynn-host letsencrypt:status

Show current Let's Encrypt configuration status.
`)
	Register("letsencrypt:enable-system-routes", runACMEEnableSystemRoutesCmd, `
usage: flynn-host letsencrypt:enable-system-routes

Enable Let's Encrypt on all system app routes.
`)
	Register("letsencrypt:disable-system-routes", runACMEDisableSystemRoutesCmd, `
usage: flynn-host letsencrypt:disable-system-routes

Disable Let's Encrypt on all system app routes.
`)
	Register("letsencrypt", runACMEStatusCmd, `
usage: flynn-host letsencrypt

Show current Let's Encrypt configuration status.
`)
	Register("acme:enable", runACMEEnableCmd, `
usage: flynn-host acme:enable

Enable ACME/Let's Encrypt for the cluster.
`)
	Register("acme:disable", runACMEDisableCmd, `
usage: flynn-host acme:disable

Disable ACME/Let's Encrypt for the cluster.
`)
	Register("acme:status", runACMEStatusCmd, `
usage: flynn-host acme:status

Show current ACME/Let's Encrypt configuration status.
`)
	Register("acme:enable-system-routes", runACMEEnableSystemRoutesCmd, `
usage: flynn-host acme:enable-system-routes

Enable Let's Encrypt on all system app routes.
`)
	Register("acme:disable-system-routes", runACMEDisableSystemRoutesCmd, `
usage: flynn-host acme:disable-system-routes

Disable Let's Encrypt on all system app routes.
`)
}

func runACMEConfigureCmd(args *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runACMEConfigure(args, client)
}

func runACMEEnableCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runACMEEnable(client)
}

func runACMEDisableCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runACMEDisable(client)
}

func runACMEStatusCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runACMEStatus(client)
}

func runACMEEnableSystemRoutesCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runACMEEnableSystemRoutes(client)
}

func runACMEDisableSystemRoutesCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runACMEDisableSystemRoutes(client)
}

func getControllerClient() (controller.Client, error) {
	instances, err := discoverd.GetInstances("controller", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("error discovering controller: %s", err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no controller instances found")
	}

	// Same discoverd dial as the updater: rotate across controller instances
	// instead of pinning the first registration.
	httpClient := &http.Client{Transport: &http.Transport{Dial: discoverdDial}}
	key := controllerAPIKey(instances[0].Meta)
	if key == "" {
		return nil, missingControllerKeyErr()
	}
	return controller.NewClientWithHTTP("http://controller.discoverd", key, httpClient)
}

func lookupLetsEncryptPlugin(apps []*ct.App) (*ct.App, error) {
	if app, err := plugin.LookupPluginApp(apps, "letsencrypt"); err == nil {
		return app, nil
	}
	for _, a := range apps {
		if a != nil && a.Name == "acme" && a.System() {
			return a, nil
		}
	}
	return nil, fmt.Errorf("the Let's Encrypt plugin is not installed\nInstall it with: sudo flynn-host plugin:install letsencrypt")
}

func requireLetsEncryptPlugin(client controller.Client) error {
	apps, err := client.AppList()
	if err != nil {
		return err
	}
	_, err = lookupLetsEncryptPlugin(apps)
	return err
}

func runACMEConfigure(args *docopt.Args, client controller.Client) error {
	if err := requireLetsEncryptPlugin(client); err != nil {
		return err
	}
	email := args.String["--email"]
	if email == "" {
		return fmt.Errorf("--email is required")
	}

	agreeTos := args.Bool["--agree-tos"]
	if !agreeTos {
		return fmt.Errorf("--agree-tos is required to register with Let's Encrypt")
	}

	config, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error getting ACME config: %s", err)
	}

	// Determine directory URL
	// Priority: --directory-url > --staging > existing config > default (production)
	useStaging := args.Bool["--staging"]
	directoryURL := args.String["--directory-url"]
	if directoryURL == "" {
		if useStaging {
			directoryURL = stagingACMEDirectoryURL
		} else if config.DirectoryURL != "" {
			directoryURL = config.DirectoryURL
		} else {
			directoryURL = defaultACMEDirectoryURL
		}
	}

	// Warn if using staging
	if directoryURL == stagingACMEDirectoryURL {
		fmt.Println("WARNING: Using Let's Encrypt STAGING server.")
		fmt.Println("         Certificates will NOT be trusted by browsers.")
		fmt.Println("         Use this only for testing.")
		fmt.Println()
	}

	// Generate a new ECDSA key for the ACME account
	fmt.Println("Generating ACME account key...")
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("error generating ACME account key: %s", err)
	}

	// Create ACME client and register account
	fmt.Printf("Registering account with ACME provider (%s)...\n", directoryURL)
	acmeClient, err := acmelib.NewClient(directoryURL)
	if err != nil {
		return fmt.Errorf("error creating ACME client: %s", err)
	}

	// Format contact as mailto: URL
	contact := email
	if !strings.HasPrefix(contact, "mailto:") {
		contact = "mailto:" + contact
	}

	// Register the account
	_, err = acmeClient.NewAccount(privKey, false, true, contact)
	if err != nil {
		return fmt.Errorf("error registering ACME account: %s", err)
	}

	// Encode the private key to PEM
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("error encoding private key: %s", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	}))

	// Update the config with the new account
	config.ContactEmail = email
	config.TermsOfServiceAgreed = true
	config.DirectoryURL = directoryURL
	config.AccountKey = keyPEM
	config.Enabled = true // Auto-enable when configuring

	if err := client.UpdateACMEConfig(config); err != nil {
		return fmt.Errorf("error updating ACME config: %s", err)
	}

	fmt.Println("ACME account registered and enabled successfully.")
	fmt.Println("\nEnable HTTPS on a hostname with:")
	fmt.Println("  flynn letsencrypt:enable www.example.com")
	fmt.Println("\nTo enable Let's Encrypt on all system app routes, run:")
	fmt.Println("  flynn-host letsencrypt:enable-system-routes")
	return nil
}

// enableLetsEncryptOnSystemRoutes enables Let's Encrypt on all system app HTTP routes
func enableLetsEncryptOnSystemRoutes(client controller.Client) error {
	// Get the cluster domain from the controller release
	release, err := client.GetAppRelease("controller")
	if err != nil {
		return fmt.Errorf("error getting controller release: %s", err)
	}
	clusterDomain := release.Env["DEFAULT_ROUTE_DOMAIN"]
	if clusterDomain == "" {
		return fmt.Errorf("could not determine cluster domain from controller")
	}
	fmt.Printf("Cluster domain: %s\n", clusterDomain)

	// Get all routes in the cluster
	allRoutes, err := client.RouteList()
	if err != nil {
		return fmt.Errorf("error listing routes: %s", err)
	}

	// Get all apps to check which are system apps
	apps, err := client.AppList()
	if err != nil {
		return fmt.Errorf("error listing apps: %s", err)
	}

	// Build maps for quick lookup
	appByID := make(map[string]*ct.App)
	appByName := make(map[string]*ct.App)
	for _, app := range apps {
		appByID[app.ID] = app
		appByName[app.Name] = app
	}

	var enabledCount, alreadyEnabledCount, errorCount int

	for _, route := range allRoutes {
		// Only process HTTP routes
		if route.Type != "http" {
			continue
		}

		// Extract app ID from ParentRef (format: "controller/apps/<app_id>")
		if !strings.HasPrefix(route.ParentRef, ct.RouteParentRefPrefix) {
			continue
		}
		appID := strings.TrimPrefix(route.ParentRef, ct.RouteParentRefPrefix)

		// Get the app
		app, ok := appByID[appID]
		if !ok {
			continue
		}

		// Check if this is a system app OR if this is the base cluster domain
		isSystemApp := app.System()
		isBaseClusterDomain := route.Domain == clusterDomain

		if !isSystemApp && !isBaseClusterDomain {
			continue
		}

		// Check if Let's Encrypt is already enabled
		if route.ManagedCertificateDomain != nil && *route.ManagedCertificateDomain != "" {
			label := app.Name
			if isBaseClusterDomain {
				label = app.Name + " (base domain)"
			}
			fmt.Printf("  [skip] %s: %s already enabled\n", label, route.Domain)
			alreadyEnabledCount++
			continue
		}

		// Enable managed certificate for this route
		domain := route.Domain
		route.ManagedCertificateDomain = &domain
		route.Certificate = nil
		route.LegacyTLSCert = ""
		route.LegacyTLSKey = ""

		routeID := fmt.Sprintf("%s/%s", route.Type, route.ID)
		if err := client.UpdateRoute(app.Name, routeID, route); err != nil {
			fmt.Printf("  [error] %s: %s - %s\n", app.Name, route.Domain, err)
			errorCount++
			continue
		}

		label := app.Name
		if isBaseClusterDomain {
			label = app.Name + " (base domain)"
		}
		fmt.Printf("  [enabled] %s: %s\n", label, domain)
		enabledCount++
	}

	if enabledCount == 0 && alreadyEnabledCount == 0 && errorCount == 0 {
		return fmt.Errorf("no system app HTTP routes found")
	}

	fmt.Printf("\nSummary: %d enabled, %d already configured, %d errors\n", enabledCount, alreadyEnabledCount, errorCount)

	if errorCount > 0 {
		return fmt.Errorf("%d routes failed to enable", errorCount)
	}

	return nil
}

func runACMEEnable(client controller.Client) error {
	if err := requireLetsEncryptPlugin(client); err != nil {
		return err
	}
	config, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error getting ACME config: %s", err)
	}

	if config.ContactEmail == "" || !config.HasAccountKey {
		return fmt.Errorf("ACME is not configured. Run 'flynn-host letsencrypt:configure --email=<email> --agree-tos' first.")
	}
	if !config.TermsOfServiceAgreed {
		return fmt.Errorf("You must agree to the Let's Encrypt Terms of Service. Run 'flynn-host letsencrypt:configure --email=%s --agree-tos'.", config.ContactEmail)
	}

	if config.Enabled {
		fmt.Println("ACME/Let's Encrypt is already enabled.")
		return nil
	}

	config.Enabled = true
	if err := client.UpdateACMEConfig(config); err != nil {
		return fmt.Errorf("error enabling ACME: %s", err)
	}

	fmt.Println("ACME/Let's Encrypt has been enabled for this cluster.")
	fmt.Println("Enable HTTPS on a hostname with: flynn letsencrypt:enable www.example.com")
	return nil
}

func runACMEDisable(client controller.Client) error {
	config, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error getting ACME config: %s", err)
	}

	config.Enabled = false
	if err := client.UpdateACMEConfig(config); err != nil {
		return fmt.Errorf("error disabling ACME: %s", err)
	}

	fmt.Println("ACME/Let's Encrypt has been disabled for this cluster.")
	fmt.Println("Existing managed certificates will continue to work but will not be renewed.")
	return nil
}

func runACMEStatus(client controller.Client) error {
	config, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error getting ACME config: %s", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 1, 2, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "ACME/Let's Encrypt Configuration")
	fmt.Fprintln(w, "=================================")
	fmt.Fprintf(w, "Enabled:\t%t\n", config.Enabled)
	fmt.Fprintf(w, "Contact Email:\t%s\n", valueOrNone(config.ContactEmail))
	fmt.Fprintf(w, "Terms of Service Agreed:\t%t\n", config.TermsOfServiceAgreed)

	// Show directory URL with staging indicator
	dirURL := config.DirectoryURL
	if dirURL == "" {
		fmt.Fprintf(w, "Directory URL:\t%s (default)\n", defaultACMEDirectoryURL)
	} else if dirURL == stagingACMEDirectoryURL {
		fmt.Fprintf(w, "Directory URL:\t%s (STAGING - certs not trusted!)\n", dirURL)
	} else if dirURL == defaultACMEDirectoryURL {
		fmt.Fprintf(w, "Directory URL:\t%s (production)\n", dirURL)
	} else {
		fmt.Fprintf(w, "Directory URL:\t%s (custom)\n", dirURL)
	}

	if config.UpdatedAt != nil {
		fmt.Fprintf(w, "Last Updated:\t%s\n", config.UpdatedAt.Format(time.RFC3339))
	}

	return nil
}

func runACMEEnableSystemRoutes(client controller.Client) error {
	if err := requireLetsEncryptPlugin(client); err != nil {
		return err
	}
	// Check if ACME is enabled
	config, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error getting ACME config: %s", err)
	}
	if !config.Enabled {
		return fmt.Errorf("ACME/Let's Encrypt is not enabled for this cluster.\nRun 'flynn-host letsencrypt:configure --email=<email> --agree-tos' first.")
	}

	fmt.Println("Enabling Let's Encrypt for all system app routes...")
	if err := enableLetsEncryptOnSystemRoutes(client); err != nil {
		return err
	}

	fmt.Println("\nLet's Encrypt has been enabled for all system app routes.")
	fmt.Println("TLS certificates will be automatically provisioned.")
	fmt.Println("\nThe TLS pin in ~/.flynnrc is no longer needed since all system routes")
	fmt.Println("will use CA-signed Let's Encrypt certificates.")
	fmt.Println("Run 'flynn cluster:refresh --clear' to remove it.")
	return nil
}

func runACMEDisableSystemRoutes(client controller.Client) error {
	fmt.Println("Disabling Let's Encrypt for all system app routes...")
	if err := disableLetsEncryptOnSystemRoutes(client); err != nil {
		return err
	}

	fmt.Println("\nLet's Encrypt has been disabled for all system app routes.")
	fmt.Println("Routes will no longer have automatic TLS certificate provisioning.")
	return nil
}

// disableLetsEncryptOnSystemRoutes disables Let's Encrypt on all system app HTTP routes
func disableLetsEncryptOnSystemRoutes(client controller.Client) error {
	// Get the cluster domain from the controller release
	release, err := client.GetAppRelease("controller")
	if err != nil {
		return fmt.Errorf("error getting controller release: %s", err)
	}
	clusterDomain := release.Env["DEFAULT_ROUTE_DOMAIN"]
	if clusterDomain == "" {
		return fmt.Errorf("could not determine cluster domain from controller")
	}
	fmt.Printf("Cluster domain: %s\n", clusterDomain)

	// Get all routes in the cluster
	allRoutes, err := client.RouteList()
	if err != nil {
		return fmt.Errorf("error listing routes: %s", err)
	}

	// Get all apps to check which are system apps
	apps, err := client.AppList()
	if err != nil {
		return fmt.Errorf("error listing apps: %s", err)
	}

	// Build maps for quick lookup
	appByID := make(map[string]*ct.App)
	for _, app := range apps {
		appByID[app.ID] = app
	}

	var disabledCount, alreadyDisabledCount, errorCount int

	for _, route := range allRoutes {
		// Only process HTTP routes
		if route.Type != "http" {
			continue
		}

		// Extract app ID from ParentRef (format: "controller/apps/<app_id>")
		if !strings.HasPrefix(route.ParentRef, ct.RouteParentRefPrefix) {
			continue
		}
		appID := strings.TrimPrefix(route.ParentRef, ct.RouteParentRefPrefix)

		// Get the app
		app, ok := appByID[appID]
		if !ok {
			continue
		}

		// Check if this is a system app OR if this is the base cluster domain
		isSystemApp := app.System()
		isBaseClusterDomain := route.Domain == clusterDomain

		if !isSystemApp && !isBaseClusterDomain {
			continue
		}

		// Check if Let's Encrypt is already disabled
		if route.ManagedCertificateDomain == nil || *route.ManagedCertificateDomain == "" {
			label := app.Name
			if isBaseClusterDomain {
				label = app.Name + " (base domain)"
			}
			fmt.Printf("  [skip] %s: %s already disabled\n", label, route.Domain)
			alreadyDisabledCount++
			continue
		}

		// Disable managed certificate for this route
		route.ManagedCertificateDomain = nil
		route.Certificate = nil
		route.LegacyTLSCert = ""
		route.LegacyTLSKey = ""

		routeID := fmt.Sprintf("%s/%s", route.Type, route.ID)
		if err := client.UpdateRoute(app.Name, routeID, route); err != nil {
			fmt.Printf("  [error] %s: %s - %s\n", app.Name, route.Domain, err)
			errorCount++
			continue
		}

		label := app.Name
		if isBaseClusterDomain {
			label = app.Name + " (base domain)"
		}
		fmt.Printf("  [disabled] %s: %s\n", label, route.Domain)
		disabledCount++
	}

	if disabledCount == 0 && alreadyDisabledCount == 0 && errorCount == 0 {
		return fmt.Errorf("no system app HTTP routes found")
	}

	fmt.Printf("\nSummary: %d disabled, %d already disabled, %d errors\n", disabledCount, alreadyDisabledCount, errorCount)

	if errorCount > 0 {
		return fmt.Errorf("%d routes failed to disable", errorCount)
	}

	return nil
}

func valueOrNone(s string) string {
	if s == "" {
		return "(not configured)"
	}
	return s
}

func valueOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
