package main

import (
	"github.com/flynn/flynn/cli/login"
	"github.com/flynn/go-docopt"
)

func init() {
	register("login", func(args *docopt.Args) error {
		return login.Run(args, flagCluster)
	}, `
usage: flynn login [-p] [-n <cluster-name>] [--controller-url=<url>] [--oob-code] [-f] [<issuer-or-cluster>]

Authenticate with the Flynn dashboard (OAuth).

With no arguments, uses the default cluster from ~/.flynnrc (or the only cluster if there is just one). The cluster entry must include a DashboardURL from flynn cluster add or an OAuthURL from a prior login.

If <issuer-or-cluster> contains "://" or starts with http:, it is treated as the dashboard / OAuth issuer URL. Otherwise it is a cluster name in ~/.flynnrc (same as flynn -c <cluster> login).

Options:
	--controller-url=<url>                controller URL when adding a cluster interactively
	-n --cluster-name=<cluster-name>      local ~/.flynnrc cluster name for the new entry (default is "default")
	-f --force                            force creation of cluster even if the name already exists
	-p --prompt                           prompt for selection of controller cluster from the OAuth audience list
	--oob-code                            do not attempt to use a browser and local HTTP listener for OAuth
`)
}
