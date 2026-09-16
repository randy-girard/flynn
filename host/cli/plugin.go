package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
)

const pluginUsage = `
usage: flynn-host plugin install [--no-build] [--rebuild] [--ref=REF] [--github-org=ORG] [--auto-tls] <source>
       flynn-host plugin uninstall [--force] [--github-org=ORG] <plugin>
       flynn-host plugin list
       flynn-host plugin credentials set github [--token-file=FILE] [--api=URL]
       flynn-host plugin credentials unset github
       flynn-host plugin credentials show github
       flynn-host plugin <plugin> route
       flynn-host plugin <plugin> route add http [-s <service>] [-p <port>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--sticky] [--leader] [--no-drain-backends] [--disable-keep-alives] [<domain>]
       flynn-host plugin <plugin> route add tcp [-s <service>] [-p <port>] [--leader] [--no-drain-backends]
       flynn-host plugin <plugin> route update <id> [-s <service>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--no-auto-tls] [--sticky] [--no-sticky] [--leader] [--no-leader] [--disable-keep-alives] [--enable-keep-alives]
       flynn-host plugin <plugin> route remove <id>

Commands:
	install       Install a plugin from a local path, alias, or GitHub URL
	uninstall     Remove an installed plugin app, webhooks, and optional uninstall hook
	list          List plugins installed on this cluster
	credentials   Store a GitHub token for private or draft release assets
	route         List, add, update, or remove routes for an installed plugin

Options:
	--no-build         Fail if dist/ is missing instead of running script/plugin-build
	--rebuild          Run script/plugin-build even if dist/ already exists (local only)
	--ref=REF          GitHub release tag (default: latest published, or plugins.json ref)
	--github-org=ORG   GitHub org for aliases (default: FLYNN_PLUGIN_GITHUB_ORG or randy-girard)
	--force            Uninstall a resource-provider even if other apps still use it
	--auto-tls         Enable Let's Encrypt on HTTP routes (requires ACME)
	--no-auto-tls      Disable Let's Encrypt on an existing HTTP route
	--token-file=FILE  Read the GitHub token from a file (otherwise stdin)
	--api=URL          GitHub API base (GitHub Enterprise)
	-s, --service=<service>    service name to route to (defaults to the plugin name)
	-c, --tls-cert=<tls-cert>  path to PEM encoded certificate for TLS (http only)
	-k, --tls-key=<tls-key>    path to PEM encoded private key for TLS (http only)
	-p, --port=<port>          port to accept traffic on

The installer is generic: it reads flynn-plugin.json, uploads layers to the
cluster blobstore, deploys the system app, registers a provider only when
kind is resource-provider, and registers flynn-host webhooks declared in
the manifest. If cluster ACME is already enabled, HTTP plugin routes get
Let's Encrypt automatically. Manifest auto_tls still requests TLS when you
are not passing --auto-tls (warns if ACME is off). --auto-tls fails if ACME
is off. After install, flynn-host plugin <name> route is the same shape as
flynn route (list / add http / update / remove) scoped to that plugin app.
Uninstall reverses that: optional hooks.uninstall, plugin webhooks, then
DeleteApp (routes and exclusive resources). Resource-provider plugins with
provisioned resources still in use refuse unless --force. Flynn does not
special-case plugin names. Re-running install deploys a new release and
scales the previous release to zero so old jobs leave discoverd.
Local
checkouts are used when present. Otherwise
short names pull a published GitHub Release named flynn-plugin-<name> (or the
repo declared by a sibling checkout / installed plugin). GitHub installs never
build on the cluster; they unpack release assets (image layers plus any
hooks.install scripts) rather than a git checkout.

Configure extra aliases and org in /etc/flynn/plugins.json. Installed plugins
are recorded in /etc/flynn/installed-plugins.json so cluster backup, restore,
and sirenia repair do not hardcode appliance names. Private repos use
flynn-host plugin credentials, FLYNN_PLUGIN_GITHUB_TOKEN, or GITHUB_TOKEN.

Examples:

    $ flynn-host plugin install ../flynn-plugin-redis
    $ flynn-host plugin install redis --ref v20260914.0
    $ flynn-host plugin install dashboard --auto-tls
    $ flynn-host plugin dashboard route
    $ flynn-host plugin dashboard route add http --auto-tls
    $ flynn-host plugin dashboard route update http/<id> --auto-tls
    $ flynn-host plugin install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0
    $ flynn-host plugin uninstall dashboard
    $ flynn-host plugin uninstall redis --force
    $ flynn-host plugin credentials set github --token-file /root/github.token
    $ flynn-host plugin list
`

func init() {
	Register("plugin", runPlugin, pluginUsage)
}

func runPlugin(args *docopt.Args) error {
	switch {
	case args.Bool["install"]:
		return runPluginInstall(args)
	case args.Bool["uninstall"]:
		return runPluginUninstall(args)
	case args.Bool["list"]:
		return runPluginList()
	case args.Bool["credentials"]:
		return runPluginCredentials(args)
	case args.Bool["route"]:
		return runPluginRoute(args)
	}
	return nil
}

func runPluginInstall(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	in := &plugin.Installer{
		Client: client,
		HTTP:   discoverdHTTPClient(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}
	return in.Install(plugin.InstallOptions{
		Source:    args.String["<source>"],
		Ref:       args.String["--ref"],
		GitHubOrg: args.String["--github-org"],
		Cwd:       cwd,
		NoBuild:   args.Bool["--no-build"],
		Rebuild:   args.Bool["--rebuild"],
		AutoTLS:   args.Bool["--auto-tls"],
	})
}

func runPluginUninstall(args *docopt.Args) error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	in := &plugin.Installer{
		Client: client,
		HTTP:   discoverdHTTPClient(),
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}
	return in.Uninstall(plugin.UninstallOptions{
		Name:      args.String["<plugin>"],
		Force:     args.Bool["--force"],
		Cwd:       cwd,
		GitHubOrg: args.String["--github-org"],
	})
}

func runPluginList() error {
	client, err := controllerClient()
	if err != nil {
		return err
	}
	apps, err := client.AppList()
	if err != nil {
		return err
	}
	_ = plugin.WriteInstalled("", plugin.ListInstalled(apps))
	w := tabwriter.NewWriter(os.Stdout, 0, 8, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tKIND\tCLI\tSOURCE\tREF")
	n := 0
	for _, app := range apps {
		if !app.Plugin() {
			continue
		}
		kind := app.Meta[plugin.MetaPluginKind]
		cliName := ""
		if c := plugin.CLIFromApp(app); c != nil {
			cliName = c.Command
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", app.Name, kind, cliName, app.Meta[plugin.MetaPluginSource], app.Meta[plugin.MetaPluginRef])
		n++
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "no plugins installed")
	}
	return nil
}

func runPluginCredentials(args *docopt.Args) error {
	host := "github.com"
	switch {
	case args.Bool["set"]:
		token, err := readCredentialToken(args.String["--token-file"])
		if err != nil {
			return err
		}
		if err := plugin.SetGitHubCredentials("", host, token, args.String["--api"]); err != nil {
			return err
		}
		fmt.Println("github credentials set")
		return nil
	case args.Bool["unset"]:
		if err := plugin.UnsetGitHubCredentials("", host); err != nil {
			return err
		}
		fmt.Println("github credentials unset")
		return nil
	case args.Bool["show"]:
		ok, err := plugin.CredentialsSet("", host)
		if err != nil {
			return err
		}
		if ok {
			fmt.Println("github credentials: set")
		} else {
			fmt.Println("github credentials: unset")
		}
		return nil
	}
	return nil
}

func readCredentialToken(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		tok := strings.TrimSpace(string(data))
		if tok == "" {
			return "", fmt.Errorf("token file is empty")
		}
		return tok, nil
	}
	st, _ := os.Stdin.Stat()
	if st != nil && st.Mode()&os.ModeCharDevice != 0 {
		fmt.Fprintln(os.Stderr, "read GitHub token from stdin (token is not echoed in argv)")
	}
	tok, err := plugin.ReadToken(os.Stdin)
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", fmt.Errorf("token is empty; pass --token-file or pipe a token on stdin")
	}
	return tok, nil
}
