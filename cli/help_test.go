package main

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/clihelp"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestFormatHelpListsCoreChildren(t *testing.T) {
	got := formatHelp("env")
	for _, want := range []string{"usage: flynn env", "Commands:", "env:get", "env:set", "env:unset"} {
		if !strings.Contains(got, want) {
			t.Fatalf("env help missing %q:\n%s", want, got)
		}
	}
	leaf := formatHelp("env:get")
	if strings.Contains(leaf, "\nCommands:") {
		t.Fatalf("env:get is a leaf:\n%s", leaf)
	}
	le := formatHelp("letsencrypt")
	for _, want := range []string{"usage: flynn letsencrypt", "Commands:", "letsencrypt:enable", "letsencrypt:disable", "letsencrypt:status"} {
		if !strings.Contains(le, want) {
			t.Fatalf("letsencrypt help missing %q:\n%s", want, le)
		}
	}
}

func TestFormatHelpListsPluginChildren(t *testing.T) {
	cat := datastorePluginCatalog()
	cases := []struct {
		topic string
		want  []string
		hide  []string
	}{
		{"redis", []string{"usage: flynn redis", "manage redis databases", "Commands:", "redis:dump", "redis:restore", "redis:cli"}, []string{"dump dump"}},
		{"mysql", []string{"usage: flynn mysql", "manage mysql databases", "Commands:", "mysql:dump", "mysql:restore", "mysql:cli"}, []string{"dump dump"}},
		{"mongodb", []string{"usage: flynn mongodb", "manage mongodb databases", "Commands:", "mongodb:dump", "mongodb:restore", "mongodb:cli"}, []string{"dump dump"}},
		{"clickhouse", []string{"usage: flynn clickhouse", "manage clickhouse clusters", "Commands:", "clickhouse:cli", "clickhouse:databases"}, []string{"clickhouse:databases:create"}},
		{"pg", []string{"usage: flynn pg", "manage an isolated postgres instance", "Commands:", "pg:psql", "Open psql against this instance", "pg:info", "Show leader, followers, and lag", "pg:dump"}, []string{"pg:cli", "pg:psql  psql"}},
		{"scheduler", []string{"usage: flynn scheduler", "manage scheduled jobs for an app", "Commands:", "scheduler:list", "scheduler:add", "scheduler:remove", "scheduler:enable", "scheduler:disable", "scheduler:info"}, nil},
		{"kafka", []string{"kafka:topics", "kafka:consumer-groups:create"}, []string{"kafka:topics:create"}},
		{"kafka:topics", []string{"kafka:topics:create"}, nil},
	}
	for _, tc := range cases {
		got := formatHelpWith(tc.topic, cat)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s help missing %q:\n%s", tc.topic, want, got)
			}
		}
		for _, hide := range tc.hide {
			if strings.Contains(got, hide) {
				t.Errorf("%s help should not contain %q:\n%s", tc.topic, hide, got)
			}
		}
	}
}

func TestRootHelpHidesPluginActions(t *testing.T) {
	got := mergePluginUsage(formatCoreRootHelp(), datastorePluginCatalog(), nil)
	for _, parent := range []string{"redis", "mysql", "mongodb", "clickhouse", "kafka", "scheduler"} {
		if !strings.Contains(got, "\n  "+parent) {
			t.Errorf("root must list %s:\n%s", parent, got)
		}
		if strings.Contains(got, "\t"+parent) {
			t.Errorf("Plugins list must indent %s like Commands (two spaces), not a tab:\n%s", parent, got)
		}
	}
	for _, nested := range []string{"redis:dump", "mysql:dump", "mongodb:dump", "clickhouse:databases", "kafka:topics", "scheduler:list"} {
		if strings.Contains(got, nested) {
			t.Errorf("root must not list %s:\n%s", nested, got)
		}
	}
}

func datastorePluginCatalog() *plugin.Catalog {
	return &plugin.Catalog{Commands: []plugin.CLI{
		{
			Command: "redis",
			Usage:   "manage redis databases",
			Doc:     "usage: flynn redis dump",
			Actions: []plugin.CLIAction{
				{Name: "redis-cli", Args: []string{"redis-cli"}},
				{Name: "dump", Args: []string{"dump"}},
				{Name: "restore", Args: []string{"restore"}},
			},
		},
		{
			Command: "mysql",
			Usage:   "manage mysql databases",
			Doc:     "usage: flynn mysql dump",
			Actions: []plugin.CLIAction{
				{Name: "console", Args: []string{"mysql"}},
				{Name: "dump", Args: []string{"dump"}},
				{Name: "restore", Args: []string{"restore"}},
			},
		},
		{
			Command: "mongodb",
			Usage:   "manage mongodb databases",
			Doc:     "usage: flynn mongodb dump",
			Actions: []plugin.CLIAction{
				{Name: "mongo", Args: []string{"mongo"}},
				{Name: "dump", Args: []string{"dump"}},
				{Name: "restore", Args: []string{"restore"}},
			},
		},
		{
			Command: "clickhouse",
			Usage:   "manage clickhouse clusters",
			Doc:     "usage: flynn clickhouse client",
			Actions: []plugin.CLIAction{
				{Name: "client", Args: []string{"client"}},
				{Name: "databases", Args: []string{"databases"}},
			},
		},
		{
			Command: "kafka",
			Usage:   "manage kafka clusters",
			Doc:     "usage: flynn kafka topics",
			Actions: []plugin.CLIAction{
				{Name: "topics", Args: []string{"topics"}},
				{Name: "topics create", Args: []string{"topics", "create"}},
				{Name: "consumer-groups create", Args: []string{"consumer-groups", "create"}},
			},
		},
		{
			Command: "pg",
			Usage:   "manage an isolated postgres instance",
			Doc: `usage: flynn pg
       flynn pg:create <database>
       flynn pg:psql [--] [<argument>...]
       flynn pg:dump [-q] [-f <file>]

Commands:
	create    Create a logical database on this instance
	psql      Open psql against this instance
	info      Show leader, followers, and lag
	dump      Dump this instance in custom format

Options:
	-f, --file=<file>  dump file
	-q, --quiet        don't print progress
`,
			Actions: []plugin.CLIAction{
				{Name: "psql", Args: []string{"psql"}},
				{Name: "info", Args: []string{"info"}},
				{Name: "dump", Args: []string{"dump"}},
				{Name: "create", Args: []string{"create"}, Append: "<database>"},
			},
		},
		{
			Command: "scheduler",
			Usage:   "manage scheduled jobs for an app",
			Doc:     "usage: flynn scheduler list",
			Actions: []plugin.CLIAction{
				{Name: "list", Args: []string{"list"}},
				{Name: "add", Args: []string{"add"}},
				{Name: "remove", Args: []string{"remove"}},
				{Name: "enable", Args: []string{"enable"}},
				{Name: "disable", Args: []string{"disable"}},
				{Name: "info", Args: []string{"info"}},
			},
		},
	}}
}

func TestFormatHelpLoginMentionsDashboard(t *testing.T) {
	got := formatHelp("login")
	for _, want := range []string{"Authenticate with the Flynn dashboard", "OAuth", "Examples:", "--oob-code"} {
		if !strings.Contains(got, want) {
			t.Fatalf("login help missing %q:\n%s", want, got)
		}
	}
}

func TestFormatHelpUserCreateDocumentsFlags(t *testing.T) {
	got := formatHelp("user:create")
	for _, want := range []string{"Options:", "--handle", "--password", "--admin", "Examples:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("user:create help missing %q:\n%s", want, got)
		}
	}
}

func TestFormatHelpUserChildrenHaveDescriptions(t *testing.T) {
	got := formatHelp("user")
	for _, want := range []string{
		"usage: flynn user",
		"Commands:",
		"user:list",
		"List controller users",
		"user:info",
		"Show a controller user",
		"user:disable",
		"Disable a controller user",
		"user:enable",
		"Enable a disabled controller user",
		"user:admin",
		"Grant cluster administrator",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("user help missing %q:\n%s", want, got)
		}
	}
}

func TestFormatHelpPluginLeafIsSpecific(t *testing.T) {
	cat := datastorePluginCatalog()
	psql := formatHelpWith("pg:psql", cat)
	for _, want := range []string{"usage: flynn pg:psql", "flynn pg psql", "Open psql against this instance"} {
		if !strings.Contains(psql, want) {
			t.Fatalf("pg:psql help missing %q:\n%s", want, psql)
		}
	}
	for _, hide := range []string{"Show leader", "dump file", "Commands:", "pg:create", "Create a logical database"} {
		if strings.Contains(psql, hide) {
			t.Fatalf("pg:psql help should not contain %q:\n%s", hide, psql)
		}
	}

	create := formatHelpWith("pg:create", cat)
	for _, want := range []string{"usage: flynn pg:create <database>", "flynn pg create <database>", "Create a logical database on this instance"} {
		if !strings.Contains(create, want) {
			t.Fatalf("pg:create help missing %q:\n%s", want, create)
		}
	}
	for _, hide := range []string{"Open psql", "dump file", "Commands:", "pg:dump"} {
		if strings.Contains(create, hide) {
			t.Fatalf("pg:create help should not contain %q:\n%s", hide, create)
		}
	}

	dump := formatHelpWith("pg:dump", cat)
	for _, want := range []string{"usage: flynn pg:dump", "--file", "--quiet", "Dump this instance in custom format"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("pg:dump help missing %q:\n%s", want, dump)
		}
	}
	if strings.Contains(dump, "Open psql") || strings.Contains(dump, "pg:create") {
		t.Fatalf("pg:dump help leaked sibling command:\n%s", dump)
	}
}

func TestHelpTopicPluginSpaceAlias(t *testing.T) {
	if got := helpTopic("env", nil); got != "env" {
		t.Fatalf("env: %q", got)
	}
	if got := helpTopic("env", []string{"set", "--help"}); got != "env:set" {
		t.Fatalf("env set --help: %q", got)
	}
	if got := helpTopic("plugin", []string{"--help"}); got != "plugin" {
		t.Fatalf("plugin --help: %q", got)
	}
}

func TestAllCommandsHaveHelp(t *testing.T) {
	for name, cmd := range commands {
		if strings.TrimSpace(cmd.usage) == "" {
			t.Errorf("%s has empty usage", name)
			continue
		}
		got := formatHelp(name)
		if !strings.Contains(got, "flynn "+name) {
			if target, ok := topAliases[name]; !ok || !strings.Contains(got, "flynn "+target) {
				t.Errorf("%s help missing %q:\n%s", name, "flynn "+name, got)
			}
		}
		if clihelp.ShortDescription(cmd.usage) == "" {
			t.Errorf("%s has no help description", name)
		}
	}
}

func TestFormatHelpDocumentsFlagsAndExamples(t *testing.T) {
	quota := formatHelp("account:quota:set")
	for _, want := range []string{"Options:", "--apps", "--processes", "--memory", "--resources", "--collaborators"} {
		if !strings.Contains(quota, want) {
			t.Fatalf("account:quota:set help missing %q:\n%s", want, quota)
		}
	}
	plug := formatHelp("plugin")
	for _, want := range []string{"Options:", "--known", "--check"} {
		if !strings.Contains(plug, want) {
			t.Fatalf("plugin help missing %q:\n%s", want, plug)
		}
	}
	unexpose := formatHelp("resource:unexpose")
	if !strings.Contains(unexpose, "Options:") || !strings.Contains(unexpose, "--domain") {
		t.Fatalf("resource:unexpose help missing --domain Options:\n%s", unexpose)
	}
	vol := formatHelp("volume:show")
	if !strings.Contains(vol, "Options:") || !strings.Contains(vol, "--json") {
		t.Fatalf("volume:show help missing --json Options:\n%s", vol)
	}
	add := formatHelp("resource:add")
	if !strings.Contains(add, "Examples:") || !strings.Contains(add, "resource:add postgres") {
		t.Fatalf("resource:add help missing postgres example:\n%s", add)
	}
	token := formatHelp("token:create")
	if !strings.Contains(token, "Examples:") || !strings.Contains(token, "token:create") {
		t.Fatalf("token:create help missing examples:\n%s", token)
	}
	ctx := formatHelp("context:use")
	if !strings.Contains(ctx, "Examples:") || !strings.Contains(ctx, "context:use") {
		t.Fatalf("context:use help missing examples:\n%s", ctx)
	}
}

func TestPluginActionHelpCoversActions(t *testing.T) {
	spec := postgresHelpCLI()
	cat := &plugin.Catalog{Commands: []plugin.CLI{spec}}
	for _, a := range spec.Actions {
		full := plugin.ColonName(spec.Command, a.Name)
		got := formatHelpWith(full, cat)
		if !strings.Contains(got, "usage: flynn "+full) {
			t.Errorf("%s help missing usage:\n%s", full, got)
		}
		if strings.TrimSpace(spec.ActionHelp(full)) == "" {
			t.Errorf("%s ActionHelp is empty", full)
		}
		if strings.Contains(got, "\nCommands:") {
			t.Errorf("%s dumped the plugin Commands list:\n%s", full, got)
		}
		desc := clihelp.CommandDescriptions(spec.Doc)[a.Name]
		if desc == "" {
			desc = clihelp.CommandDescriptions(spec.Doc)[strings.TrimPrefix(full, spec.Command+":")]
		}
		if desc != "" && !strings.Contains(got, desc) {
			t.Errorf("%s help missing description %q:\n%s", full, desc, got)
		}
	}
}

func postgresHelpCLI() plugin.CLI {
	return plugin.CLI{
		Command: "pg",
		Usage:   "manage an isolated postgres instance",
		Doc: `usage: flynn pg
       flynn pg:create <database>
       flynn pg:info
       flynn pg:follow [--runtime <name>]
       flynn pg:wait <resource>
       flynn pg:promote <follower>
       flynn pg:unfollow <follower>
       flynn pg:upgrade
       flynn pg:dump [-q] [-f <file>]
       flynn pg:restore [-q] [-f <file>]
       flynn pg:psql [--] [<argument>...]

Commands:
	create    Create a logical database on this instance
	info      Show leader, followers, and lag
	follow    Create a streaming read-only follower
	wait      Block until follower lag is zero
	promote   Make a follower writable and rewrite the primary URL
	unfollow  Stop replication and leave a standalone writable copy
	upgrade   Follow, wait, promote, then recreate followers
	dump      Dump this instance in custom format
	restore   Restore a dump taken with pg dump
	psql      Open psql against this instance
`,
		Actions: []plugin.CLIAction{
			{Name: "create", Append: "<database>"},
			{Name: "info"},
			{Name: "follow"},
			{Name: "wait", Append: "<resource>"},
			{Name: "promote", Append: "<follower>"},
			{Name: "unfollow", Append: "<follower>"},
			{Name: "upgrade"},
			{Name: "dump"},
			{Name: "restore"},
			{Name: "psql"},
		},
	}
}
