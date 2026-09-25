package cli

import (
	"fmt"
	"os"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/plugin"
)

const pluginInstallUsage = `
usage: flynn-host plugin:install [--no-build] [--rebuild] [--ref=REF] [--github-org=ORG] [--auto-tls] [--allow-external-layers] [--yes] <source>

Install a plugin from a local path, alias, or GitHub URL.

Options:
	--no-build                 Fail if dist/ is missing instead of running script/plugin-build
	--rebuild                  Run script/plugin-build even if dist/ already exists (local only)
	--ref=REF                  GitHub release tag matching this Flynn vYYYYMMDD.N (or vYYYYMMDD.N.B)
	--github-org=ORG           GitHub org for aliases (default: FLYNN_PLUGIN_GITHUB_ORG or randy-girard)
	--auto-tls                 Enable Let's Encrypt on HTTP routes (requires ACME)
	--allow-external-layers    Fetch non-GitHub image.json layer URLs without GitHub credentials
	--yes                      Accept cluster-secret injection without a prompt

Plugin jobs receive CONTROLLER_KEY, DISCOVERD_AUTH_KEY, and access-token
keys so appliance admin HTTP and discoverd can authenticate. That is
cluster-admin equivalent. Flynn catalog plugins (plugin:list --known)
print that note and continue. Third-party plugins prompt on a TTY;
non-interactive installs must pass --yes. Re-running install on an
existing plugin deploys a new release and scales the previous release
to zero so old jobs leave discoverd.

The installer is generic: it reads flynn-plugin.json, uploads layers to the
cluster blobstore, deploys the system app, registers a provider only when
kind is resource-provider, and registers flynn-host webhooks declared in
the manifest. Squashfs already in this host's layer-cache (flynn-host
update or a previous plugin install) is reused: GitHub is not re-downloaded
and the Flynn OS layer is not copied into the plugin blobstore prefix when
Flynn already publishes a LayerURL. If cluster ACME is already enabled, HTTP plugin routes get
Let's Encrypt automatically. Manifest auto_tls still requests TLS when you
are not passing --auto-tls (warns if ACME is off). --auto-tls fails if ACME
is off. After install, flynn-host plugin:route <name> is the same shape as
flynn route (list / add http / update / remove) scoped to that plugin app.
Install does not special-case a plugin name; short names come from the
official catalog. Local checkouts are used when present. Otherwise short
names pull a published GitHub Release from the official catalog
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
    $ flynn-host plugin:install https://github.com/acme/flynn-plugin-widget.git --yes --ref v20260922.0.0
    $ flynn-host plugin:install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0.0
`

const pluginUpdateUsage = `
usage: flynn-host plugin:update [--no-build] [--rebuild] [--ref=REF] [--github-org=ORG] [--auto-tls] [--allow-external-layers] [--yes] <plugin>

Deploy a new release of an already-installed plugin. update requires the
plugin app to already exist. Update runs hooks.upgrade when declared
(not hooks.install) and does not re-ask setup prompts. --yes accepts
cluster-secret injection without a prompt (needed for third-party plugins
on a non-TTY). --ref is a plugin GitHub tag (vYYYYMMDD.N.B). Omit it to
install the newest published calver that matches this cluster's Flynn
version (vYYYYMMDD.N). A plugin tagged v20260919.0.3 installs on Flynn
v20260919.0; v20260920.0 is refused.

Examples:

    $ flynn-host plugin:update dashboard --ref v20260916.3.1
    $ flynn-host plugin:update-all
`

const pluginUpdateAllUsage = `
usage: flynn-host plugin:update-all [--github-org=ORG] [--auto-tls] [--allow-external-layers] [--yes]

Update every installed official plugin to the highest compatible GitHub tag
for this Flynn version (vYYYYMMDD.N.B; never a newer Flynn date.N). Continues
past individual failures and prints a per-plugin result.

Options:
	--github-org=ORG           GitHub org for aliases (default: FLYNN_PLUGIN_GITHUB_ORG or randy-girard)
	--auto-tls                 Enable Let's Encrypt on HTTP routes (requires ACME)
	--allow-external-layers    Fetch non-GitHub image.json layer URLs without GitHub credentials
	--yes                      Accept cluster-secret injection without a prompt

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
usage: flynn-host plugin:list [--known] [--check]

List plugins installed on this cluster, including the installed VERSION
(GitHub tag). --check queries GitHub for the highest compatible tag for
this Flynn version and shows UPDATE and STATUS (current, update, or -).

Options:
	--known            List the public first-party catalog Flynn can plugin:install. Private first-party plugins are listed in a footer and cannot be installed from this catalog.
	--check            Compare installed versions to published GitHub releases

Examples:

    $ flynn-host plugin:list
    $ flynn-host plugin:list --check
    $ flynn-host plugin:list --known
`

const pluginCredentialsUsage = `
usage: flynn-host plugin:credentials

Manage GitHub credentials for plugin releases.

The host argument is required: github (github.com) or a GitHub Enterprise
hostname. set never puts the token in argv. show never prints the token.

Commands:
  plugin:credentials:set    Store a GitHub token for plugin releases
  plugin:credentials:unset  Remove stored GitHub plugin credentials
  plugin:credentials:show   Show whether GitHub plugin credentials are set
`

const pluginCredentialsSetUsage = `
usage: flynn-host plugin:credentials:set <host> [--token-file=FILE] [--api=URL]

Store a GitHub token for private or draft release assets. <host> is github
(github.com) or a GitHub Enterprise hostname. On a TTY with no --token-file
and no piped stdin, you are prompted to paste the token (input is hidden).

Options:
	--token-file=FILE  Read the GitHub token from a file
	--api=URL          GitHub API base (GitHub Enterprise)

Examples:

    $ flynn-host plugin:credentials:set github
    $ flynn-host plugin:credentials:set github --token-file /root/github.token
    $ cat /root/github.token | flynn-host plugin:credentials:set github
    $ flynn-host plugin:credentials:set git.example.com --api https://git.example.com/api/v3 --token-file /root/ghe.token
`

const pluginCredentialsUnsetUsage = `
usage: flynn-host plugin:credentials:unset <host>

Remove stored GitHub plugin credentials. <host> is github (github.com) or a
GitHub Enterprise hostname. Prints whether credentials were removed or
nothing was stored.

Examples:

    $ flynn-host plugin:credentials:unset github
`

const pluginCredentialsShowUsage = `
usage: flynn-host plugin:credentials:show <host>

Show whether GitHub plugin credentials are set. <host> is github (github.com)
or a GitHub Enterprise hostname. Prints set/unset and the stored API URL
when present; never prints the token.

Examples:

    $ flynn-host plugin:credentials:show github
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
		Source:              args.String["<source>"],
		Ref:                 args.String["--ref"],
		GitHubOrg:           args.String["--github-org"],
		Cwd:                 cwd,
		NoBuild:             args.Bool["--no-build"],
		Rebuild:             args.Bool["--rebuild"],
		AutoTLS:             args.Bool["--auto-tls"],
		AllowExternalLayers: args.Bool["--allow-external-layers"],
		Yes:                 args.Bool["--yes"],
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
		Source:              args.String["<plugin>"],
		Ref:                 args.String["--ref"],
		GitHubOrg:           args.String["--github-org"],
		Cwd:                 cwd,
		NoBuild:             args.Bool["--no-build"],
		Rebuild:             args.Bool["--rebuild"],
		AutoTLS:             args.Bool["--auto-tls"],
		AllowExternalLayers: args.Bool["--allow-external-layers"],
		Yes:                 args.Bool["--yes"],
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
		GitHubOrg:           args.String["--github-org"],
		Cwd:                 cwd,
		AutoTLS:             args.Bool["--auto-tls"],
		AllowExternalLayers: args.Bool["--allow-external-layers"],
		Yes:                 args.Bool["--yes"],
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
	installed := plugin.ListInstalled(apps)
	_ = plugin.WriteInstalled("", installed)
	check := args.Bool["--check"]
	var rows []plugin.ListedPlugin
	if check {
		in := &plugin.Installer{Stdout: os.Stdout, Stderr: os.Stderr}
		rows = in.CheckUpdates(installed, "")
	} else {
		rows = plugin.ListedFromInstalled(installed)
	}
	if err := plugin.WriteHostPluginTable(os.Stdout, rows, check); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no plugins installed")
	}
	if check {
		plugin.WritePluginUpdateNotes(os.Stderr, rows)
	}
	return nil
}

func runPluginListKnown() error {
	return plugin.WriteKnownPlugins(os.Stdout, plugin.DefaultGitHubOrg(), plugin.KnownPlugins())
}
