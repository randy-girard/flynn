package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	acmelib "github.com/eggsampler/acme/v3"
	controller "github.com/flynn/flynn/controller/client"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/go-docopt"
)

const defaultACMEDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"

func init() {
	Register("acme", runACME, `
usage: flynn-host acme
       flynn-host acme configure --email=<email> [--agree-tos] [--directory-url=<url>]
       flynn-host acme enable
       flynn-host acme disable
       flynn-host acme status

Manage ACME/Let's Encrypt configuration for the cluster.

ACME must be configured and enabled before automatic TLS certificates can be
provisioned for routes using the --auto-tls flag.

Commands:
    With no arguments, shows the current ACME configuration status.

    configure  Configure ACME with a contact email address
    enable     Enable ACME/Let's Encrypt for the cluster
    disable    Disable ACME/Let's Encrypt for the cluster
    status     Show current ACME configuration status

Options:
    --email=<email>          Contact email for Let's Encrypt account (required for configure)
    --agree-tos              Agree to the Let's Encrypt Terms of Service
    --directory-url=<url>    ACME directory URL (defaults to Let's Encrypt production)

Examples:
    $ flynn-host acme configure --email=admin@example.com --agree-tos
    $ flynn-host acme enable
    $ flynn-host acme status
`)
}

func runACME(args *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}

	if args.Bool["configure"] {
		return runACMEConfigure(args, client)
	} else if args.Bool["enable"] {
		return runACMEEnable(client)
	} else if args.Bool["disable"] {
		return runACMEDisable(client)
	}
	// Default: show status
	return runACMEStatus(client)
}

func getControllerClient() (controller.Client, error) {
	instances, err := discoverd.GetInstances("controller", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("error discovering controller: %s", err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no controller instances found")
	}
	inst := instances[0]
	return controller.NewClient("http://"+inst.Addr, inst.Meta["AUTH_KEY"])
}

func runACMEConfigure(args *docopt.Args, client controller.Client) error {
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
	directoryURL := args.String["--directory-url"]
	if directoryURL == "" {
		directoryURL = config.DirectoryURL
	}
	if directoryURL == "" {
		directoryURL = defaultACMEDirectoryURL
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
	fmt.Println("You can now use --auto-tls when adding routes to automatically provision TLS certificates.")
	return nil
}

func runACMEEnable(client controller.Client) error {
	config, err := client.GetACMEConfig()
	if err != nil {
		return fmt.Errorf("error getting ACME config: %s", err)
	}

	if config.ContactEmail == "" || !config.HasAccountKey {
		return fmt.Errorf("ACME is not configured. Run 'flynn-host acme configure --email=<email> --agree-tos' first.")
	}
	if !config.TermsOfServiceAgreed {
		return fmt.Errorf("You must agree to the Let's Encrypt Terms of Service. Run 'flynn-host acme configure --email=%s --agree-tos'.", config.ContactEmail)
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
	fmt.Println("You can now use --auto-tls when adding routes to automatically provision TLS certificates.")
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
	fmt.Fprintf(w, "Directory URL:\t%s\n", valueOrDefault(config.DirectoryURL, "https://acme-v02.api.letsencrypt.org/directory (default)"))
	if config.UpdatedAt != nil {
		fmt.Fprintf(w, "Last Updated:\t%s\n", config.UpdatedAt.Format(time.RFC3339))
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
