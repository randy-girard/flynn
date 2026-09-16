package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/flynn/flynn/pkg/plugin"
)

func TestFilterPluginUsageKeepsCoreCommands(t *testing.T) {
	usage := strings.Join([]string{
		"usage: flynn [--version] [--help] <command> [<args>]",
		"",
		"Commands:",
		"   ps          list jobs",
		"   help        show help",
		"",
		"See 'flynn help <command>' for more information on a specific command.",
	}, "\n")

	hidden := mergePluginUsage(usage, nil, errors.New("no cluster"))
	if !strings.Contains(hidden, "ps") || !strings.Contains(hidden, "help") {
		t.Fatalf("core commands must remain:\n%s", hidden)
	}
	for _, cmd := range []string{"redis", "mysql", "mongodb", "kafka", "clickhouse"} {
		if plugin.IsCorePluginCommand(cmd) {
			t.Fatalf("%s must not be a compiled plugin command", cmd)
		}
	}
}

func TestMergePluginUsageAddsCatalogCommands(t *testing.T) {
	usage := strings.Join([]string{
		"Commands:",
		"   ps          list jobs",
		"",
		"See 'flynn help <command>' for more information on a specific command.",
	}, "\n")
	cat := &plugin.Catalog{Commands: []plugin.CLI{
		{
			Command: "redis",
			Usage:   "manage redis databases",
			Doc:     "usage: flynn redis dump",
			Actions: []plugin.CLIAction{{Name: "dump", Args: []string{"/bin/dump"}}},
		},
		{Command: "stub"}, // provider-only: not runnable, must not appear
	}}
	got := mergePluginUsage(usage, cat, nil)
	if !strings.Contains(got, "redis") || !strings.Contains(got, "manage redis databases") {
		t.Fatalf("installed plugin CLI must appear:\n%s", got)
	}
	if strings.Contains(got, "stub") {
		t.Fatalf("non-runnable catalog entries must not appear:\n%s", got)
	}
	if !strings.Contains(got, "ps") {
		t.Fatal("core command dropped")
	}
	idxRedis := strings.Index(got, "redis")
	idxSee := strings.Index(got, "See 'flynn help")
	if idxRedis < 0 || idxSee < 0 || idxRedis > idxSee {
		t.Fatalf("plugin commands belong in the Commands list:\n%s", got)
	}
}

func TestMissingPluginCommand(t *testing.T) {
	if err := requirePluginCommand("ps"); err != nil {
		t.Fatalf("core CLI must not require a plugin: %v", err)
	}
	err := missingPluginCommand("mongodb", nil, errors.New("offline"))
	if err == nil || !strings.Contains(err.Error(), "flynn-host plugin install mongodb") {
		t.Fatalf("got %v", err)
	}
	err = missingPluginCommand("mongodb", &plugin.Catalog{}, nil)
	if err == nil {
		t.Fatal("empty catalog")
	}
	if err := missingPluginCommand("mysql", &plugin.Catalog{Commands: []plugin.CLI{{Command: "mysql"}}}, nil); err != nil {
		t.Fatal(err)
	}
}
