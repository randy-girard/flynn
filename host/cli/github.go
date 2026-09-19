package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/githubapp"
)

func init() {
	Register("github", runGitHubStatusCmd, `
usage: flynn-host github

Show cluster GitHub App configuration status.
`)
	Register("github:configure", runGitHubConfigureCmd, `
usage: flynn-host github:configure --app-id=<id> --private-key-file=<path> --webhook-secret=<secret> [--slug=<slug>] [--client-id=<id>] [--client-secret=<secret>] [--api-url=<url>]

Save the cluster GitHub App used for Heroku-style GitHub deploys.

Create the GitHub App first (see flynn-host github:setup). Required
repository permissions: Contents (read), Metadata (read), Commit statuses
(read), Checks (read). Subscribe to Push, Check suite, Check run, and Status.

Options:
    --app-id=<id>                 GitHub App ID
    --private-key-file=<path>     PEM private key downloaded from GitHub
    --webhook-secret=<secret>     Same secret configured on the GitHub App
    --slug=<slug>                 GitHub App slug (for the install URL)
    --client-id=<id>              Optional OAuth client ID
    --client-secret=<secret>      Optional OAuth client secret
    --api-url=<url>               GitHub API root (default https://api.github.com)

Examples:
    $ flynn-host github:configure --app-id=123456 --private-key-file /root/flynn-github.pem --webhook-secret s3cret --slug flynn-deploy
`)
	Register("github:status", runGitHubStatusCmd, `
usage: flynn-host github:status

Show cluster GitHub App configuration status.
`)
	Register("github:setup", runGitHubSetupCmd, `
usage: flynn-host github:setup

Print the exact GitHub App permissions, events, and setup steps.
`)
	Register("github:disable", runGitHubDisableCmd, `
usage: flynn-host github:disable

Clear the cluster GitHub App credentials.
`)
}

func runGitHubConfigureCmd(args *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runGitHubConfigure(args, client, os.ReadFile)
}

func runGitHubStatusCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runGitHubStatus(client, os.Stdout)
}

func runGitHubSetupCmd(_ *docopt.Args) error {
	return runGitHubSetup(os.Stdout, os.Getenv("DEFAULT_ROUTE_DOMAIN"))
}

func runGitHubDisableCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runGitHubDisable(client)
}

type gitHubAppClient interface {
	GetGitHubApp() (*ct.GitHubAppConfig, error)
	UpdateGitHubApp(config *ct.GitHubAppConfig) error
}

func runGitHubConfigure(args *docopt.Args, client gitHubAppClient, readFile func(string) ([]byte, error)) error {
	appID, err := strconv.ParseInt(args.String["--app-id"], 10, 64)
	if err != nil || appID <= 0 {
		return fmt.Errorf("--app-id must be a positive integer")
	}
	path := args.String["--private-key-file"]
	pemBytes, err := readFile(path)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	if _, err := githubapp.ParseRSAPrivateKey(pemBytes); err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	secret := args.String["--webhook-secret"]
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("--webhook-secret is required")
	}
	cfg := &ct.GitHubAppConfig{
		AppID:         appID,
		Slug:          args.String["--slug"],
		PrivateKey:    string(pemBytes),
		WebhookSecret: secret,
		ClientID:      args.String["--client-id"],
		ClientSecret:  args.String["--client-secret"],
		APIURL:        args.String["--api-url"],
	}
	if err := client.UpdateGitHubApp(cfg); err != nil {
		return err
	}
	fmt.Printf("GitHub App %d configured. Install it on the GitHub account that owns deployable repos, then connect a repo with flynn github:connect owner/repo.\n", appID)
	return nil
}

func runGitHubStatus(client gitHubAppClient, w io.Writer) error {
	cfg, err := client.GetGitHubApp()
	if err != nil {
		return err
	}
	state := "not configured"
	if cfg.Configured {
		state = "configured"
	}
	fmt.Fprintf(w, "Status:          %s\n", state)
	fmt.Fprintf(w, "App ID:          %d\n", cfg.AppID)
	fmt.Fprintf(w, "Slug:            %s\n", emptyDash(cfg.Slug))
	fmt.Fprintf(w, "Private key:     %s\n", boolSet(cfg.HasPrivateKey))
	fmt.Fprintf(w, "Webhook secret:  %s\n", boolSet(cfg.HasWebhookSecret))
	fmt.Fprintf(w, "Webhook URL:     %s\n", emptyDash(cfg.WebhookURL))
	fmt.Fprintf(w, "Git webhook URL: %s\n", emptyDash(cfg.GitWebhookURL))
	fmt.Fprintf(w, "Install URL:     %s\n", emptyDash(cfg.InstallURL))
	return nil
}

func runGitHubSetup(w io.Writer, domain string) error {
	guide := githubapp.Setup(domain, "", "")
	fmt.Fprintln(w, "GitHub App setup for Flynn deploys")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Permissions:")
	for _, p := range guide.Permissions {
		fmt.Fprintf(w, "  - %s: %s — %s\n", p.Name, p.Access, p.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Subscribe to events:")
	for _, e := range guide.Events {
		fmt.Fprintf(w, "  - %s — %s\n", e.Name, e.Description)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Steps:")
	for i, s := range guide.Steps {
		fmt.Fprintf(w, "  %d. %s\n     %s\n", i+1, s.Title, s.Body)
	}
	return nil
}

func runGitHubDisable(client gitHubAppClient) error {
	return client.UpdateGitHubApp(&ct.GitHubAppConfig{})
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func boolSet(v bool) string {
	if v {
		return "set"
	}
	return "unset"
}
