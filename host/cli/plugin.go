package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
)

func init() {
	Register("plugin", runPlugin, `
usage: flynn-host plugin install [--no-build] [--rebuild] [--ref=REF] [--github-org=ORG] <source>
       flynn-host plugin list
       flynn-host plugin credentials set github [--token-file=FILE] [--api=URL]
       flynn-host plugin credentials unset github
       flynn-host plugin credentials show github

Commands:
	install       Install a plugin from a local path, alias, or GitHub URL
	list          List plugins installed on this cluster
	credentials   Store a GitHub token for private or draft release assets

Options:
	--no-build         Fail if dist/ is missing instead of running script/plugin-build
	--rebuild          Run script/plugin-build even if dist/ already exists (local only)
	--ref=REF          GitHub release tag (default: latest published, or plugins.json ref)
	--github-org=ORG   GitHub org for aliases (default: FLYNN_PLUGIN_GITHUB_ORG or randy-girard)
	--token-file=FILE  Read the GitHub token from a file (otherwise stdin)
	--api=URL          GitHub API base (GitHub Enterprise)

The installer is generic: it reads flynn-plugin.json, uploads layers to the
cluster blobstore, deploys the system app, and registers a provider only when
kind is resource-provider. Local checkouts are used when present. Otherwise
aliases (redis, …) pull a published GitHub Release. GitHub installs never
build on the cluster.

Configure aliases and org in /etc/flynn/plugins.json. Private repos use
flynn-host plugin credentials, FLYNN_PLUGIN_GITHUB_TOKEN, or GITHUB_TOKEN.

Examples:

    $ flynn-host plugin install ../flynn-plugin-redis
    $ flynn-host plugin install redis --ref v20260914.0
    $ flynn-host plugin install https://github.com/randy-girard/flynn-plugin-redis.git --ref v20260914.0
    $ flynn-host plugin credentials set github --token-file /root/github.token
    $ flynn-host plugin list
`)
}

func runPlugin(args *docopt.Args) error {
	switch {
	case args.Bool["install"]:
		return runPluginInstall(args)
	case args.Bool["list"]:
		return runPluginList()
	case args.Bool["credentials"]:
		return runPluginCredentials(args)
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
	}
	return in.Install(plugin.InstallOptions{
		Source:    args.String["<source>"],
		Ref:       args.String["--ref"],
		GitHubOrg: args.String["--github-org"],
		Cwd:       cwd,
		NoBuild:   args.Bool["--no-build"],
		Rebuild:   args.Bool["--rebuild"],
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
