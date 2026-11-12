package cli

import (
	"fmt"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	Register("postgres:availability", runPostgresAvailability, `
usage: flynn-host postgres:availability [<mode>]

Show or set Postgres continuous availability using the local cluster key.
manual creates no platform replica. system keeps one platform replica per
primary when another host can take the volume. Hosted and self-hosted
clusters use the same values. This does not deploy the plugin, restart
user apps, or change tenancy mode.

Examples:

    $ flynn-host postgres:availability
    $ flynn-host postgres:availability system
    $ flynn-host postgres:availability manual
`)
}

func runPostgresAvailability(args *docopt.Args) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	mode := strings.TrimSpace(args.String["<mode>"])
	if mode == "" {
		var current ct.PostgresAvailability
		if err := v.Get("/cluster/postgres-availability", &current); err != nil {
			return err
		}
		fmt.Printf("mode: %s\n", current.Mode)
		return nil
	}
	if err := tenancy.ValidatePostgresAvailability(mode); err != nil {
		return err
	}
	return v.Put("/cluster/postgres-availability", map[string]string{"mode": mode}, &ct.PostgresAvailability{})
}
