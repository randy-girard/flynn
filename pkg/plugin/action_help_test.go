package plugin

import (
	"strings"
	"testing"
)

func TestActionHelpIsSpecificToTheCommand(t *testing.T) {
	c := &CLI{
		Command: "pg",
		Usage:   "manage an isolated postgres instance",
		Doc: `usage: flynn pg
       flynn pg:create <database>
       flynn pg:follow [--runtime <name>]
       flynn pg:dump [-q] [-f <file>]
       flynn pg:psql [--] [<argument>...]

Each resource is its own Postgres instance.
The colon form (flynn pg:psql) is canonical. The space form (flynn pg psql) is a fallback.
flynn pg:create adds a logical database on that server (CREATE DATABASE), not a new Flynn resource.

Options:
	-f, --file=<file>  dump file (stdout/stdin if omitted)
	-q, --quiet        don't print progress
	--runtime=<name>   database runtime for a new follower

Commands:
	create    Create a logical database on this instance
	follow    Create a streaming read-only follower
	dump      Dump this instance in custom format
	psql      Open psql against this instance

Examples:

    $ flynn pg:psql

    $ flynn pg:dump -f postgres.dump
`,
		Actions: []CLIAction{
			{Name: "create", Append: "<database>"},
			{Name: "follow"},
			{Name: "dump"},
			{Name: "psql"},
		},
	}

	create := c.ActionHelp("pg:create")
	for _, want := range []string{
		"usage: flynn pg:create <database>",
		"flynn pg create <database>",
		"Create a logical database on this instance",
		"flynn pg:create adds a logical database",
	} {
		if !strings.Contains(create, want) {
			t.Fatalf("pg:create help missing %q:\n%s", want, create)
		}
	}
	for _, hide := range []string{"Open psql", "dump file", "--runtime", "flynn pg:dump", "Commands:"} {
		if strings.Contains(create, hide) {
			t.Fatalf("pg:create help should not contain %q:\n%s", hide, create)
		}
	}

	dump := c.ActionHelp("pg:dump")
	for _, want := range []string{
		"usage: flynn pg:dump [-q] [-f <file>]",
		"Dump this instance in custom format",
		"--file",
		"--quiet",
		"$ flynn pg:dump -f postgres.dump",
	} {
		if !strings.Contains(dump, want) {
			t.Fatalf("pg:dump help missing %q:\n%s", want, dump)
		}
	}
	if strings.Contains(dump, "--runtime") || strings.Contains(dump, "Open psql") || strings.Contains(dump, "pg:create") {
		t.Fatalf("pg:dump help leaked sibling command:\n%s", dump)
	}

	psql := c.ActionHelp("pg:psql")
	for _, want := range []string{"usage: flynn pg:psql", "flynn pg psql", "Open psql against this instance", "$ flynn pg:psql"} {
		if !strings.Contains(psql, want) {
			t.Fatalf("pg:psql help missing %q:\n%s", want, psql)
		}
	}
	if strings.Contains(psql, "dump file") || strings.Contains(psql, "Create a logical database on this instance") {
		t.Fatalf("pg:psql help leaked sibling command:\n%s", psql)
	}

	follow := c.ActionHelp("pg:follow")
	if !strings.Contains(follow, "--runtime") {
		t.Fatalf("pg:follow should include --runtime:\n%s", follow)
	}
	if strings.Contains(follow, "--file") {
		t.Fatalf("pg:follow should not include dump --file:\n%s", follow)
	}
}

func TestActionHelpSynthesizesMissingUsage(t *testing.T) {
	c := &CLI{
		Command: "pg",
		Doc:     "usage: flynn pg\n\nCommands:\n\twait      Block until follower lag is zero\n",
		Actions: []CLIAction{{Name: "wait", Append: "<resource>"}},
	}
	got := c.ActionHelp("pg:wait")
	for _, want := range []string{"usage: flynn pg:wait <resource>", "flynn pg wait <resource>", "Block until follower lag is zero"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestActionHelpKafkaNested(t *testing.T) {
	c := &CLI{
		Command: "kafka",
		Doc:     "usage: flynn kafka:topics:create <topic>\n\nCommands:\n\tcreate    Create a topic\n",
		Actions: []CLIAction{{Name: "topics create", Append: "<topic>"}},
	}
	got := c.ActionHelp("kafka:topics:create")
	for _, want := range []string{"usage: flynn kafka:topics:create <topic>", "flynn kafka topics create <topic>", "Create a topic"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}
