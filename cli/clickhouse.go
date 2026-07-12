package main

import (
	"fmt"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/go-docopt"
)

func clickhouseAdmin(args ...string) []string {
	return append([]string{"/bin/flynn-clickhouse", "admin", "clickhouse-client"}, args...)
}

func createDatabaseQuery(name, cluster string) string {
	return fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` ON CLUSTER `%s` ENGINE = Atomic",
		escapeClickhouseIdentifier(name), escapeClickhouseIdentifier(cluster),
	)
}

func dropDatabaseQuery(name, cluster string) string {
	return fmt.Sprintf(
		"DROP DATABASE IF EXISTS `%s` ON CLUSTER `%s` SYNC",
		escapeClickhouseIdentifier(name), escapeClickhouseIdentifier(cluster),
	)
}

func escapeClickhouseIdentifier(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			out = append(out, '`', '`')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

func init() {
	register("clickhouse", runClickhouse, `
usage: flynn clickhouse client [--] [<argument>...]
       flynn clickhouse databases
       flynn clickhouse databases create <database>
       flynn clickhouse databases info <database>
       flynn clickhouse databases destroy <database>

Manage a Flynn ClickHouse cluster: connect to the server and manage replicated
databases.

Databases must be created with this tool before they can be used on a replicated
cluster. Database DDL is executed with ON CLUSTER so schema changes are applied
to every replica.

Commands:
	client              Open a console to the ClickHouse server.
	databases           List user-created databases.
	databases create    Create a replicated database on the cluster.
	databases info      Show tables in a database.
	databases destroy   Delete a database from every replica.

Examples:

    $ flynn clickhouse client

    $ flynn clickhouse databases create analytics

    $ flynn clickhouse databases info analytics

    $ flynn clickhouse databases destroy analytics
`)
}

func runClickhouse(args *docopt.Args, client controller.Client) error {
	config, err := getAppClickhouseRunConfig(client)
	if err != nil {
		return err
	}

	switch {
	case args.Bool["client"]:
		return runClickhouseClient(args, client, config)
	case args.Bool["databases"]:
		return runClickhouseDatabases(args, client, config)
	}
	return nil
}

func getAppClickhouseRunConfig(client controller.Client) (*runConfig, error) {
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return nil, fmt.Errorf("error getting app release: %s", err)
	}
	return getClickhouseRunConfig(client, mustApp(), appRelease)
}

func getClickhouseRunConfig(client controller.Client, app string, appRelease *ct.Release) (*runConfig, error) {
	clickhouseApp := appRelease.Env["FLYNN_CLICKHOUSE"]
	if clickhouseApp == "" {
		return nil, fmt.Errorf("No clickhouse cluster found. Provision one with `flynn resource add clickhouse`")
	}

	clickhouseRelease, err := client.GetAppRelease(clickhouseApp)
	if err != nil {
		return nil, fmt.Errorf("error getting clickhouse release: %s", err)
	}

	host := appRelease.Env["CLICKHOUSE_HOST"]
	if host == "" {
		host = "leader." + clickhouseApp + ".discoverd"
	}
	port := appRelease.Env["CLICKHOUSE_PORT"]
	if port == "" {
		port = "9000"
	}

	return &runConfig{
		App:        app,
		Release:    clickhouseRelease.ID,
		ReleaseEnv: true,
		Env: map[string]string{
			"CLICKHOUSE_HOST": host,
			"CLICKHOUSE_PORT": port,
		},
		DisableLog: true,
		Exit:       true,
	}, nil
}

func runClickhouseClient(args *docopt.Args, client controller.Client, config *runConfig) error {
	config.Args = clickhouseAdmin(args.All["<argument>"].([]string)...)
	return runJob(client, *config)
}

func runClickhouseDatabases(args *docopt.Args, client controller.Client, config *runConfig) error {
	database := args.String["<database>"]
	cluster := "flynn"

	switch {
	case args.Bool["create"]:
		config.Args = clickhouseAdmin("--query", createDatabaseQuery(database, cluster))
	case args.Bool["info"]:
		query := fmt.Sprintf(
			"SELECT name, engine FROM system.tables WHERE database = '%s' ORDER BY name FORMAT PrettyCompact",
			escapeClickhouseString(database),
		)
		config.Args = clickhouseAdmin("--query", query)
	case args.Bool["destroy"]:
		config.Args = clickhouseAdmin("--query", dropDatabaseQuery(database, cluster))
	default:
		config.Args = clickhouseAdmin("--query",
			"SELECT name FROM system.databases WHERE name NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema', 'default') ORDER BY name FORMAT PrettyCompact",
		)
	}
	return runJob(client, *config)
}

func escapeClickhouseString(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
