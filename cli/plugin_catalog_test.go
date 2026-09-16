package main

import (
	"errors"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
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

func TestCLIUsageParsesHelpAndOptionalCommand(t *testing.T) {
	parse := func(argv []string) *docopt.Args {
		t.Helper()
		args, err := docopt.Parse(cliUsage, argv, false, "", true, false)
		if err != nil {
			t.Fatalf("parse %q: %v", argv, err)
		}
		return args
	}

	cmd, cmdArgs := positionalArgs(parse([]string{}))
	if cmd != "" || len(cmdArgs) != 0 {
		t.Fatalf("bare flynn: cmd=%q args=%q", cmd, cmdArgs)
	}

	args := parse([]string{"--help"})
	if !helpFlag(args) {
		t.Fatal("flynn --help")
	}
	cmd, _ = positionalArgs(args)
	if cmd != "" {
		t.Fatalf("--help must not steal <command>: %q", cmd)
	}

	args = parse([]string{"-h"})
	if !helpFlag(args) {
		t.Fatal("flynn -h")
	}

	cmd, cmdArgs = positionalArgs(parse([]string{"help"}))
	if cmd != "help" || len(cmdArgs) != 0 {
		t.Fatalf("flynn help: %q %q", cmd, cmdArgs)
	}

	cmd, cmdArgs = positionalArgs(parse([]string{"redis", "dump"}))
	if cmd != "redis" || strings.Join(cmdArgs, " ") != "dump" {
		t.Fatalf("flynn redis dump: %q %q", cmd, cmdArgs)
	}

	old := flagCluster
	t.Cleanup(func() { flagCluster = old })
	flagCluster = ""
	if err := applyGlobalFlags(parse([]string{"-c", "prod", "--help"})); err != nil {
		t.Fatal(err)
	}
	if flagCluster != "prod" {
		t.Fatalf(" -c must apply before plugin help, got %q", flagCluster)
	}
}

func TestUsageCommandNamesFromRootUsage(t *testing.T) {
	names := usageCommandNames(cliUsage)
	if _, ok := names["See"]; ok {
		t.Fatal("footer See line must not count as a command")
	}
	if _, ok := names["help"]; !ok {
		t.Fatal("help")
	}
	if _, ok := names["plugins"]; !ok {
		t.Fatal("plugins")
	}
	if _, ok := names["redis"]; ok {
		t.Fatal("redis must not be compiled into root usage")
	}
}

func TestMergePluginUsageAddsRedisToRootUsage(t *testing.T) {
	cat := &plugin.Catalog{Commands: []plugin.CLI{
		{
			Command: "redis",
			Usage:   "manage redis databases",
			Doc:     "usage: flynn redis dump",
			Actions: []plugin.CLIAction{{Name: "dump", Args: []string{"/bin/dump"}}},
		},
	}}
	got := mergePluginUsage(cliUsage, cat, nil)
	if !strings.Contains(got, "redis") || !strings.Contains(got, "manage redis databases") {
		t.Fatalf("flynn --help must list installed redis:\n%s", got)
	}
	idxRedis := strings.Index(got, "\tredis")
	idxSee := strings.Index(got, "See 'flynn help")
	if idxRedis < 0 || idxSee < 0 || idxRedis > idxSee {
		t.Fatalf("redis belongs in the Commands list:\n%s", got)
	}
}

func TestWritePluginTable(t *testing.T) {
	var buf strings.Builder
	n := writePluginTable(&buf, nil)
	if n != 0 || !strings.Contains(buf.String(), "NAME") {
		t.Fatalf("empty: n=%d %q", n, buf.String())
	}

	buf.Reset()
	n = writePluginTable(&buf, []*ct.App{
		{Name: "router", Meta: map[string]string{"flynn-system-app": "true"}},
		{
			Name: "redis",
			Meta: map[string]string{
				"flynn-plugin":      "true",
				"flynn-plugin-kind": "resource-provider",
				"flynn-plugin-ref":  "v20260915.0",
				"flynn-plugin-cli":  `{"command":"redis","usage":"manage redis databases"}`,
			},
		},
	})
	if n != 1 {
		t.Fatalf("n=%d", n)
	}
	got := buf.String()
	for _, want := range []string{"redis", "resource-provider", "manage redis databases", "v20260915.0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "router") {
		t.Fatalf("system apps that are not plugins:\n%s", got)
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
