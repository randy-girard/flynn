package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/plugin"
)

const pluginInstallUsage = `
usage: flynn-host plugin:install [--no-build] [--rebuild] [--ref=REF] [--github-org=ORG] [--auto-tls] <source>

Install a plugin from a local path, alias, or GitHub URL.

Options:
	--no-build         Fail if dist/ is missing instead of running script/plugin-build
	--rebuild          Run script/plugin-build even if dist/ already exists (local only)
	--ref=REF          GitHub release tag matching this Flynn vYYYYMMDD.N (or vYYYYMMDD.N.B)
	--github-org=ORG   GitHub org for aliases (default: FLYNN_PLUGIN_GITHUB_ORG or randy-girard)
	--auto-tls         Enable Let's Encrypt on HTTP routes (requires ACME)

The installer is generic: it reads flynn-plugin.json, uploads layers to the
cluster blobstore, deploys the system app, registers a provider only when
kind is resource-provider, and registers flynn-host webhooks declared in
the manifest. If cluster ACME is already enabled, HTTP plugin routes get
Let's Encrypt automatically. Manifest auto_tls still requests TLS when you
are not passing --auto-tls (warns if ACME is off). --auto-tls fails if ACME
is off. After install, flynn-host plugin:route <name> is the same shape as
flynn route (list / add http / update / remove) scoped to that plugin app.
Install does not special-case a plugin name; short names come from the
official catalog. Re-running install on an existing plugin deploys a new
release and scales the previous release to zero so old jobs leave discoverd.
Local checkouts are used when present. Otherwise short names pull a
published GitHub Release from the official catalog
(pkg/plugin/official-plugins.json, embedded in flynn-host), a sibling
checkout, an installed plugin's github_repo, or flynn-plugin-<name>.
Override with a path, git URL, --github-org, or /etc/flynn/plugins.json.
GitHub installs never build on the cluster; they unpack release assets
(image layers plus any hooks.install scripts) rather than a git checkout.

Configure extra aliases and org in /etc/flynn/plugins.json. Installed plugins
are recorded in /etc/flynn/installed-plugins.json so cluster backup, restore,
and sirenia repair do not hardcode appliance names. Private repos use
flynn-host plugin:credentials:set github, FLYNN_PLUGIN_GITHUB_TOKEN, or GITHUB_TOKEN.

Examples:

    $ flynn-host plugin:install ../flynn-plugin-redis
    $ flynn-host plugin:install redis --ref v20260914.0.0
    $ flynn-host plugin:install dashboard --auto-tls
    $ flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0.0
`

const pluginUpdateUsage = `
usage: flynn-host plugin:update [--no-build] [--rebuild] [--ref=REF] [--github-org=ORG] [--auto-tls] <plugin>

Deploy a new release of an already-installed plugin. update requires the
plugin app to already exist. Update runs hooks.upgrade when declared
(not hooks.install) and does not re-ask setup prompts. --ref is a plugin
GitHub tag (vYYYYMMDD.N.B). Omit it to install the newest published calver
that matches this cluster's Flynn version (vYYYYMMDD.N). A plugin tagged
v20260919.0.3 installs on Flynn v20260919.0; v20260920.0 is refused.

Examples:

    $ flynn-host plugin:update dashboard --ref v20260916.3.1
    $ flynn-host plugin:update-all
`

const pluginUpdateAllUsage = `
usage: flynn-host plugin:update-all [--github-org=ORG] [--auto-tls]

Update every installed official plugin to the highest compatible GitHub tag
for this Flynn version (vYYYYMMDD.N.B; never a newer Flynn date.N). Continues
past individual failures and prints a per-plugin result.

Options:
	--github-org=ORG   GitHub org for aliases (default: FLYNN_PLUGIN_GITHUB_ORG or randy-girard)
	--auto-tls         Enable Let's Encrypt on HTTP routes (requires ACME)

Examples:

    $ flynn-host plugin:update-all
`

const pluginUninstallUsage = `
usage: flynn-host plugin:uninstall [--force] [--github-org=ORG] <plugin>

Remove an installed plugin app, webhooks, and optional uninstall hook.
Resource-provider plugins with provisioned resources still in use refuse
unless --force.

Options:
	--force            Uninstall a resource-provider even if other apps still use it
	--github-org=ORG   GitHub org for aliases

Examples:

    $ flynn-host plugin:uninstall dashboard
    $ flynn-host plugin:uninstall redis --force
`

const pluginListUsage = `
usage: flynn-host plugin:list [--known]

List plugins installed on this cluster.

Options:
	--known            List first-party plugins from the catalog shipped with Flynn

Examples:

    $ flynn-host plugin:list
    $ flynn-host plugin:list --known
`

const pluginCredentialsUsage = `
usage: flynn-host plugin:credentials

Manage GitHub credentials for plugin releases.

Commands:
  set      Store a GitHub token for plugin releases
  unset    Remove stored GitHub plugin credentials
  show     Show whether GitHub plugin credentials are set
`

const pluginCredentialsSetUsage = `
usage: flynn-host plugin:credentials:set github [--token-file=FILE] [--api=URL]

Store a GitHub token for private or draft release assets.

Options:
	--token-file=FILE  Read the GitHub token from a file (otherwise stdin)
	--api=URL          GitHub API base (GitHub Enterprise)

Examples:

    $ flynn-host plugin:credentials:set github --token-file /root/github.token
`

const pluginCredentialsUnsetUsage = `
usage: flynn-host plugin:credentials:unset github

Remove stored GitHub plugin credentials.
`

const pluginCredentialsShowUsage = `
usage: flynn-host plugin:credentials:show github

Show whether GitHub plugin credentials are set.
`

const pluginRouteUsage = `
usage: flynn-host plugin:route <plugin>
       flynn-host plugin:route <plugin> add http [-s <service>] [-p <port>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--sticky] [--leader] [--no-drain-backends] [--disable-keep-alives] [<domain>]
       flynn-host plugin:route <plugin> add tcp [-s <service>] [-p <port>] [--domain <host>] [--tls-mode <mode>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--leader] [--no-drain-backends]
       flynn-host plugin:route <plugin> update <id> [-s <service>] [-c <tls-cert> -k <tls-key>] [--auto-tls] [--no-auto-tls] [--sticky] [--no-sticky] [--leader] [--no-leader] [--disable-keep-alives] [--enable-keep-alives]
       flynn-host plugin:route <plugin> remove <id>

List, add, update, or remove routes for an installed plugin. Same shape as
flynn route, scoped to that plugin app.

Options:
	--auto-tls         Enable Let's Encrypt on HTTP routes (requires ACME)
	--no-auto-tls      Disable Let's Encrypt on an existing HTTP route
	-s, --service=<service>    service name to route to (defaults to the plugin name)
	-c, --tls-cert=<tls-cert>  path to PEM encoded certificate for TLS
	-k, --tls-key=<tls-key>    path to PEM encoded private key for TLS
	-p, --port=<port>          port to accept traffic on
	--tls-mode=<mode>          TCP TLS: off, passthrough, or terminate
	--domain=<host>            hostname stored on TCP routes

Examples:

    $ flynn-host plugin:route dashboard
    $ flynn-host plugin:route dashboard add http --auto-tls
    $ flynn-host plugin:route dashboard update http/<id> --auto-tls
`

func init() {
	Register("plugin:install", runPluginInstall, pluginInstallUsage)
	Register("plugin:update", runPluginUpdate, pluginUpdateUsage)
	Register("plugin:update-all", runPluginUpdateAll, pluginUpdateAllUsage)
	Register("plugin:uninstall", runPluginUninstall, pluginUninstallUsage)
	Register("plugin:list", runPluginList, pluginListUsage)
	Register("plugin:credentials", runPluginCredentials, pluginCredentialsUsage)
	Register("plugin:credentials:set", runPluginCredentialsSet, pluginCredentialsSetUsage)
	Register("plugin:credentials:unset", runPluginCredentialsUnset, pluginCredentialsUnsetUsage)
	Register("plugin:credentials:show", runPluginCredentialsShow, pluginCredentialsShowUsage)
	Register("plugin:credentials-set", runPluginCredentialsSet, aliasUsage("plugin:credentials:set", "plugin:credentials-set", pluginCredentialsSetUsage))
	Register("plugin:credentials-unset", runPluginCredentialsUnset, aliasUsage("plugin:credentials:unset", "plugin:credentials-unset", pluginCredentialsUnsetUsage))
	Register("plugin:credentials-show", runPluginCredentialsShow, aliasUsage("plugin:credentials:show", "plugin:credentials-show", pluginCredentialsShowUsage))
	Register("plugin:route", runPluginRoute, pluginRouteUsage)
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

func runPluginCredentials(_ *docopt.Args) error {
	fmt.Print(FormatHelp("plugin:credentials"))
	return nil
}

func runPluginUpdate(args *docopt.Args) error {
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
	return in.Update(plugin.InstallOptions{
		Source:    args.String["<plugin>"],
		Ref:       args.String["--ref"],
		GitHubOrg: args.String["--github-org"],
		Cwd:       cwd,
		NoBuild:   args.Bool["--no-build"],
		Rebuild:   args.Bool["--rebuild"],
		AutoTLS:   args.Bool["--auto-tls"],
	})
}

func runPluginUpdateAll(args *docopt.Args) error {
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
	return in.UpdateAll(plugin.InstallOptions{
		GitHubOrg: args.String["--github-org"],
		Cwd:       cwd,
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

func runPluginList(args *docopt.Args) error {
	if args.Bool["--known"] {
		return runPluginListKnown()
	}
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

func runPluginListKnown() error {
	return plugin.WriteKnownPlugins(os.Stdout, plugin.DefaultGitHubOrg(), plugin.KnownPlugins())
}

func runPluginCredentialsSet(args *docopt.Args) error {
	host := "github.com"
	token, err := readCredentialToken(args.String["--token-file"])
	if err != nil {
		return err
	}
	if err := plugin.SetGitHubCredentials("", host, token, args.String["--api"]); err != nil {
		return err
	}
	fmt.Println("github credentials set")
	return nil
}

func runPluginCredentialsUnset(_ *docopt.Args) error {
	if err := plugin.UnsetGitHubCredentials("", "github.com"); err != nil {
		return err
	}
	fmt.Println("github credentials unset")
	return nil
}

func runPluginCredentialsShow(_ *docopt.Args) error {
	ok, err := plugin.CredentialsSet("", "github.com")
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
