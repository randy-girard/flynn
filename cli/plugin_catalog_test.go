package main

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
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
	if strings.Contains(hidden, "Plugins:") {
		t.Fatalf("Plugins section must be omitted without a catalog:\n%s", hidden)
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
	if strings.Contains(got, "redis:dump") {
		t.Fatalf("root help must list the plugin parent, not redis:dump:\n%s", got)
	}
	if strings.Contains(got, "stub") {
		t.Fatalf("non-runnable catalog entries must not appear:\n%s", got)
	}
	if !strings.Contains(got, "ps") {
		t.Fatal("core command dropped")
	}
	assertPluginHelpSection(t, got, "redis")
}

func TestPluginHelpNamesUseNestedColons(t *testing.T) {
	cmd := plugin.CLI{
		Command: "kafka",
		Actions: []plugin.CLIAction{
			{Name: "topics"},
			{Name: "topics create"},
			{Name: "consumer-groups create"},
			{Name: "update-all"},
		},
	}
	got := pluginHelpNames(cmd)
	if !reflect.DeepEqual(got, []string{"kafka"}) {
		t.Fatalf("root help names got %q", got)
	}
	got = pluginActionNames(cmd)
	want := []string{
		"kafka:consumer-groups:create",
		"kafka:topics",
		"kafka:topics:create",
		"kafka:update-all",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
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
	names := usageCommandNames(formatRootHelp())
	if _, ok := names["See"]; ok {
		t.Fatal("footer See line must not count as a command")
	}
	if _, ok := names["help"]; !ok {
		t.Fatal("help")
	}
	if _, ok := names["plugin"]; !ok {
		t.Fatal("plugin")
	}
	if _, ok := names["plugin:list"]; ok {
		t.Fatal("plugin:list belongs under flynn help plugin")
	}
	if _, ok := names["update"]; !ok {
		t.Fatal("update")
	}
	if _, ok := names["volume"]; !ok {
		t.Fatal("volume")
	}
	if _, ok := names["redis"]; ok {
		t.Fatal("redis must not be compiled into root usage")
	}
}

func TestCLIUsageCommandsAreAlphabetical(t *testing.T) {
	var names []string
	inCommands := false
	for _, line := range strings.Split(formatRootHelp(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "See '") || trimmed == "Plugins:" {
			break
		}
		if trimmed == "Commands:" {
			inCommands = true
			continue
		}
		if strings.HasSuffix(trimmed, ":") && trimmed != "Options:" && trimmed != "Commands:" {
			t.Fatalf("help must not group commands under %q", trimmed)
		}
		if !inCommands || trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	if len(names) < 2 {
		t.Fatal("expected command list")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("commands not alphabetical: %v", names)
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
	got := mergePluginUsage(formatCoreRootHelp(), cat, nil)
	if !strings.Contains(got, "redis") || !strings.Contains(got, "manage redis databases") {
		t.Fatalf("flynn --help must list installed redis:\n%s", got)
	}
	if strings.Contains(got, "redis:dump") {
		t.Fatalf("root help must list the plugin parent, not redis:dump:\n%s", got)
	}
	assertPluginHelpSection(t, got, "redis")
	idxPlugin := strings.Index(got, "\n  plugin")
	idxPlugins := strings.Index(got, "Plugins:")
	if idxPlugin < 0 || idxPlugins < 0 || idxPlugin > idxPlugins {
		t.Fatalf("core plugins command belongs under Commands:\n%s", got)
	}
}

func assertPluginHelpSection(t *testing.T, got, cmd string) {
	t.Helper()
	idxCommands := strings.Index(got, "Commands:")
	idxPlugins := strings.Index(got, "Plugins:")
	idxCmd := strings.Index(got, "\n  "+cmd)
	idxSee := strings.Index(got, "See 'flynn help <command>")
	if idxCommands < 0 || idxPlugins < 0 || idxCmd < 0 || idxSee < 0 {
		t.Fatalf("missing Commands/Plugins/%s/See:\n%s", cmd, got)
	}
	if !(idxCommands < idxPlugins && idxPlugins < idxCmd && idxCmd < idxSee) {
		t.Fatalf("%s must be under Plugins, after Commands and before the footer:\n%s", cmd, got)
	}
	if !strings.Contains(got, "\n\nPlugins:\n") {
		t.Fatalf("blank line required between Commands and Plugins:\n%s", got)
	}
	if strings.Contains(got, "\t"+cmd) {
		t.Fatalf("Plugins list must indent like Commands (two spaces), not a tab:\n%s", got)
	}
}

func TestWritePluginTable(t *testing.T) {
	var buf strings.Builder
	rows := plugin.ListedFromInstalled(nil)
	if err := plugin.WriteUserPluginTable(&buf, rows, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "NAME") || !strings.Contains(buf.String(), "VERSION") {
		t.Fatalf("empty: %q", buf.String())
	}

	buf.Reset()
	rows = plugin.ListedFromInstalled(plugin.ListInstalled([]*ct.App{
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
		{
			Name: "control-ui",
			Meta: map[string]string{
				"flynn-plugin":      "true",
				"flynn-plugin-kind": "app",
				"flynn-plugin-cli":  `{"command":"control-ui","usage":"cluster UI"}`,
			},
		},
	}))
	if err := plugin.WriteUserPluginTable(&buf, rows, false); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("n=%d", len(rows))
	}
	got := buf.String()
	for _, want := range []string{"redis", "resource-provider", "manage redis databases", "v20260915.0", "control-ui", "app"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cluster UI") {
		t.Fatalf("kind: app system plugins must not show a user flynn command:\n%s", got)
	}
	if strings.Contains(got, "router") {
		t.Fatalf("system apps that are not plugins:\n%s", got)
	}
}

func TestPrintPluginListEmpty(t *testing.T) {
	var out, errBuf strings.Builder
	printPluginList(&out, &errBuf, nil, false)
	if !strings.Contains(out.String(), "NAME") {
		t.Fatalf("header: %q", out.String())
	}
	if !strings.Contains(errBuf.String(), "no plugins installed") {
		t.Fatalf("stderr: %q", errBuf.String())
	}
	errBuf.Reset()
	printPluginList(&out, &errBuf, []*ct.App{{
		Name: "redis",
		Meta: map[string]string{"flynn-plugin": "true"},
	}}, false)
	if errBuf.Len() != 0 {
		t.Fatalf("installed plugin must not print empty notice: %q", errBuf.String())
	}
}

func TestAppendCatalogCommandsBranches(t *testing.T) {
	usage := "Commands:\n\tps          list jobs\n"
	cat := &plugin.Catalog{Commands: []plugin.CLI{
		{Command: ""},
		{Command: "ps", Doc: "usage: flynn ps", Actions: []plugin.CLIAction{{Name: "ps", Args: []string{"ps"}}}},
		{Command: "redis", Doc: "usage: flynn redis", Actions: []plugin.CLIAction{{Name: "dump", Args: []string{"dump"}}}},
		{Command: "kafka", Usage: "", Doc: "usage: flynn kafka", Actions: []plugin.CLIAction{{Name: "topics", Args: []string{"topics"}}}},
	}}
	got := mergePluginUsage(usage, cat, nil)
	if !strings.Contains(got, "Plugins:") || !strings.Contains(got, "redis") || !strings.Contains(got, "plugin command") {
		t.Fatalf("default usage and Plugins section:\n%s", got)
	}
	if strings.Contains(got, "redis:dump") {
		t.Fatalf("root help must list redis, not redis:dump:\n%s", got)
	}
	if !strings.Contains(got, "list jobs\n\nPlugins:") {
		t.Fatalf("blank line between Commands and Plugins:\n%s", got)
	}
	if strings.Count(got, "\tps          list jobs") != 1 {
		t.Fatalf("compiled command must not be duplicated:\n%s", got)
	}
	if strings.Contains(got, "\tredis") || strings.Contains(got, "\tkafka") {
		t.Fatalf("Plugins list must indent like Commands (two spaces), not a tab:\n%s", got)
	}
	if !strings.Contains(got, "\n  kafka") || !strings.Contains(got, "\n  redis") {
		t.Fatalf("plugin commands must use FormatItems two-space indent:\n%s", got)
	}
}

func TestFilterPluginUsageHidesMissingCoreCommands(t *testing.T) {
	old := plugin.CorePluginCommands
	t.Cleanup(func() { plugin.CorePluginCommands = old })
	plugin.CorePluginCommands = []string{"mysql"}
	usage := "Commands:\n\tmysql       manage mysql\n\tps          list jobs\n"
	hidden := mergePluginUsage(usage, nil, errors.New("offline"))
	if strings.Contains(hidden, "mysql") || !strings.Contains(hidden, "ps") {
		t.Fatalf("missing core plugin command must be hidden:\n%s", hidden)
	}
	kept := mergePluginUsage(usage, &plugin.Catalog{Commands: []plugin.CLI{{Command: "mysql"}}}, nil)
	if !strings.Contains(kept, "mysql") {
		t.Fatalf("installed core plugin command must stay:\n%s", kept)
	}
}

func TestPluginAwareUsageWithoutCluster(t *testing.T) {
	got := formatRootHelp()
	if !strings.Contains(got, "Commands:") {
		t.Fatalf("%s", got)
	}
	if strings.Contains(got, "Plugins:") {
		t.Fatal("no cluster means no Plugins section")
	}
	if strings.Contains(got, "env:set") || strings.Contains(got, "plugin:list") {
		t.Fatalf("root help must list parents only:\n%s", got)
	}
}

func TestMissingPluginCommand(t *testing.T) {
	if err := requirePluginCommand("ps"); err != nil {
		t.Fatalf("core CLI must not require a plugin: %v", err)
	}
	err := missingPluginCommand("mongodb", nil, errors.New("offline"))
	if err == nil || !strings.Contains(err.Error(), "flynn-host plugin:install mongodb") {
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

func TestRequirePluginCommandLooksUpCatalog(t *testing.T) {
	if err := requirePluginCommand("ps"); err != nil {
		t.Fatalf("core CLI: %v", err)
	}
	old := plugin.CorePluginCommands
	t.Cleanup(func() { plugin.CorePluginCommands = old })
	plugin.CorePluginCommands = []string{"mysql"}
	err := requirePluginCommand("mysql")
	if err == nil || !strings.Contains(err.Error(), "flynn-host plugin:install mysql") {
		t.Fatalf("got %v", err)
	}
}
