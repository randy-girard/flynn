package main

import (
	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/cli/login"
)

func init() {
	register("login", func(args *docopt.Args) error {
		return login.Run(args, flagCluster)
	}, `
usage: flynn login [--email <email>] [--password <password>] [-n <cluster-name>] [--controller-url=<url>] [--oauth] [--oob-code] [-f] [<issuer-or-cluster>]

Log in to a Flynn cluster as a controller user.

The default issuer is this cluster's auth URL (https://auth.<domain>). Interactive login prompts for email and password and uses the OAuth password grant. --oauth uses browser / PKCE against the same host. After login the cluster redirects to http://127.0.0.1:8085/ so the CLI can finish. Tokens are stored per cluster name in ~/.flynn/tokens/<cluster>/flynn-cli.json so flynn -c a and flynn -c b stay logged in independently.

On a local cluster, add auth.<domain> to /etc/hosts with the same IP as controller.<domain> (hosts files have no wildcards).

With no arguments, uses the default cluster from ~/.flynnrc (or the only cluster if there is just one). Run flynn cluster:add first.

If <issuer-or-cluster> contains "://" it is treated as the controller / OAuth issuer URL. Otherwise it is a cluster name in ~/.flynnrc (same as flynn -c <cluster> login).

Non-interactive scripts should pass --email and --password, or set FLYNN_EMAIL and FLYNN_PASSWORD.

Options:
	--email=<email>                       account email
	--password=<password>                 account password
	--controller-url=<url>                controller URL when adding a cluster interactively
	-n --cluster-name=<cluster-name>      local ~/.flynnrc cluster name (default is the selected cluster)
	-f --force                            force creation of cluster even if the name already exists
	--oauth                               use browser OAuth (PKCE) instead of a password prompt
	--oob-code                            deprecated alias for --oauth

Examples:

	$ flynn login
	$ flynn login --email ada@example.com --password secret
	$ flynn login --oauth
	$ flynn -c staging login
`)
}
