package clickhouse

import (
	"fmt"
	"sort"
	"strings"
)

// systemDatabases are excluded from user-facing database listings.
var systemDatabases = map[string]struct{}{
	"system":              {},
	"INFORMATION_SCHEMA":  {},
	"information_schema":  {},
	"default":             {},
}

// DatabaseList returns the names of user-created databases.
func (p *Process) DatabaseList() ([]string, error) {
	out, err := p.run(p.OpTimeout, "clickhouse-client", p.clientArgs(
		"--query", "SELECT name FROM system.databases ORDER BY name FORMAT TSVRaw",
	)...)
	if err != nil {
		return nil, err
	}

	var databases []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, ok := systemDatabases[name]; ok {
			continue
		}
		databases = append(databases, name)
	}
	sort.Strings(databases)
	return databases, nil
}

// CreateDatabase provisions a replicated database across the cluster.
func (p *Process) CreateDatabase(name string) error {
	if name == "" {
		return fmt.Errorf("database name is required")
	}
	query := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` ON CLUSTER `%s` ENGINE = Atomic",
		escapeIdentifier(name), escapeIdentifier(p.ClusterName),
	)
	_, err := p.run(p.OpTimeout, "clickhouse-client", p.clientArgs("--query", query)...)
	return err
}

// DeleteDatabase permanently removes a database from every replica.
func (p *Process) DeleteDatabase(name string) error {
	if name == "" {
		return fmt.Errorf("database name is required")
	}
	query := fmt.Sprintf(
		"DROP DATABASE IF EXISTS `%s` ON CLUSTER `%s` SYNC",
		escapeIdentifier(name), escapeIdentifier(p.ClusterName),
	)
	_, err := p.run(p.OpTimeout, "clickhouse-client", p.clientArgs("--query", query)...)
	return err
}

// DescribeDatabase returns the row count for tables in the database.
func (p *Process) DescribeDatabase(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("database name is required")
	}
	query := fmt.Sprintf(
		"SELECT name, engine FROM system.tables WHERE database = '%s' ORDER BY name FORMAT PrettyCompact",
		escapeString(name),
	)
	out, err := p.run(p.OpTimeout, "clickhouse-client", p.clientArgs("--query", query)...)
	return string(out), err
}

func escapeIdentifier(s string) string {
	return strings.ReplaceAll(s, "`", "``")
}

func escapeString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
