package cli

import (
	"fmt"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
)

func init() {
	Register("mysql:availability", func(args *docopt.Args) error {
		return runEngineAvailability(args, "mysql", "/cluster/mysql-availability")
	}, engineAvailabilityUsage("mysql", "MySQL"))
	Register("redis:availability", func(args *docopt.Args) error {
		return runEngineAvailability(args, "redis", "/cluster/redis-availability")
	}, engineAvailabilityUsage("redis", "Redis"))
	Register("mongodb:availability", func(args *docopt.Args) error {
		return runEngineAvailability(args, "mongodb", "/cluster/mongodb-availability")
	}, quorumAvailabilityUsage("mongodb", "MongoDB"))
	Register("kafka:availability", func(args *docopt.Args) error {
		return runEngineAvailability(args, "kafka", "/cluster/kafka-availability")
	}, quorumAvailabilityUsage("kafka", "Kafka"))
	Register("clickhouse:availability", func(args *docopt.Args) error {
		return runEngineAvailability(args, "clickhouse", "/cluster/clickhouse-availability")
	}, quorumAvailabilityUsage("clickhouse", "ClickHouse"))
}

func quorumAvailabilityUsage(engine, title string) string {
	return fmt.Sprintf(`
usage: flynn-host %s:availability [<mode>]

Show or set %s continuous availability using the local cluster key.
manual starts one member and leaves %s:scale to the operator. system grows
each resource one member at a time up to three when three hosts are
registered, and up to the host count on a smaller cluster. It does not
shrink when set back to manual. Hosted and self-hosted clusters use the
same path. Tenancy mode does not change this value. This does not deploy
the plugin, restart user apps, or place members with flynn-avoid-host-ids.

Examples:

    $ flynn-host %s:availability
    $ flynn-host %s:availability system
    $ flynn-host %s:availability manual
`, engine, title, engine, engine, engine, engine)
}

func engineAvailabilityUsage(engine, title string) string {
	return fmt.Sprintf(`
usage: flynn-host %s:availability [<mode>]

Show or set %s continuous availability using the local cluster key.
manual creates no platform replica. system keeps one platform replica per
primary when another host can take the volume. Hosted and self-hosted
clusters use the same values. This does not deploy the plugin, restart
user apps, or change tenancy mode.

Examples:

    $ flynn-host %s:availability
    $ flynn-host %s:availability system
    $ flynn-host %s:availability manual
`, engine, title, engine, engine, engine)
}

func runEngineAvailability(args *docopt.Args, label, path string) error {
	v, err := hostV1()
	if err != nil {
		return err
	}
	mode := strings.TrimSpace(args.String["<mode>"])
	if mode == "" {
		var current ct.PostgresAvailability
		if err := v.Get(path, &current); err != nil {
			return err
		}
		fmt.Printf("mode: %s\n", current.Mode)
		return nil
	}
	if err := tenancy.ValidateAvailability(mode); err != nil {
		return err
	}
	return v.Put(path, map[string]string{"mode": mode}, &ct.PostgresAvailability{})
}
