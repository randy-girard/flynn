package main

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestFormatHelpListsCoreChildren(t *testing.T) {
	got := formatHelp("env")
	for _, want := range []string{"usage: flynn env", "Commands:", "get", "set", "unset"} {
		if !strings.Contains(got, want) {
			t.Fatalf("env help missing %q:\n%s", want, got)
		}
	}
	leaf := formatHelp("env:get")
	if strings.Contains(leaf, "\nCommands:") {
		t.Fatalf("env:get is a leaf:\n%s", leaf)
	}
}

func TestFormatHelpListsPluginChildren(t *testing.T) {
	cat := datastorePluginCatalog()
	cases := []struct {
		topic string
		want  []string
		hide  []string
	}{
		{"redis", []string{"usage: flynn redis", "manage redis databases", "Commands:", "dump", "restore", "cli", "redis-cli"}, []string{"redis:dump"}},
		{"mysql", []string{"usage: flynn mysql", "manage mysql databases", "Commands:", "dump", "restore", "cli", "console"}, []string{"mysql:dump"}},
		{"mongodb", []string{"usage: flynn mongodb", "manage mongodb databases", "Commands:", "dump", "restore", "cli", "mongo"}, []string{"mongodb:dump"}},
		{"clickhouse", []string{"usage: flynn clickhouse", "manage clickhouse clusters", "Commands:", "cli", "client", "databases"}, []string{"clickhouse:databases:create"}},
		{"scheduler", []string{"usage: flynn scheduler", "manage scheduled jobs for an app", "Commands:", "list", "add", "remove", "enable", "disable", "info"}, []string{"scheduler:list"}},
		{"kafka", []string{"topics", "consumer-groups:create"}, []string{"topics:create"}},
		{"kafka:topics", []string{"create"}, nil},
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
		if !strings.Contains(got, "\t"+parent) {
			t.Errorf("root must list %s:\n%s", parent, got)
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
