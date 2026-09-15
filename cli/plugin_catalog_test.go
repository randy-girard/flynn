package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/flynn/flynn/pkg/plugin"
)

func TestFilterPluginUsageHidesUninstalledCommands(t *testing.T) {
	usage := strings.Join([]string{
		"usage: flynn [--version] [--help] <command> [<args>]",
		"",
		"Commands:",
		"   redis       manage redis resources",
		"   mysql       manage mysql resources",
		"   ps          list jobs",
		"   help        show help",
	}, "\n")

	hidden := filterPluginUsage(usage, nil, errors.New("no cluster"))
	if strings.Contains(hidden, "redis") || strings.Contains(hidden, "mysql") {
		t.Fatalf("catalog error must hide plugin commands:\n%s", hidden)
	}
	if !strings.Contains(hidden, "ps") || !strings.Contains(hidden, "help") {
		t.Fatalf("core commands must remain:\n%s", hidden)
	}

	installed := filterPluginUsage(usage, &plugin.Catalog{Commands: []plugin.CLI{{Command: "redis"}}}, nil)
	if !strings.Contains(installed, "redis") {
		t.Fatalf("installed redis must stay:\n%s", installed)
	}
	if strings.Contains(installed, "mysql") {
		t.Fatalf("uninstalled mysql must hide:\n%s", installed)
	}
}

func TestMissingPluginCommand(t *testing.T) {
	if err := requirePluginCommand("ps"); err != nil {
		t.Fatalf("core CLI must not require a plugin: %v", err)
	}
	err := missingPluginCommand("redis", nil, errors.New("offline"))
	if err == nil || !strings.Contains(err.Error(), "flynn-host plugin install redis") {
		t.Fatalf("got %v", err)
	}
	err = missingPluginCommand("redis", &plugin.Catalog{}, nil)
	if err == nil {
		t.Fatal("empty catalog")
	}
	if err := missingPluginCommand("redis", &plugin.Catalog{Commands: []plugin.CLI{{Command: "redis"}}}, nil); err != nil {
		t.Fatal(err)
	}
}
