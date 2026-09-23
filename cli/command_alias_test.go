package main

import (
	"reflect"
	"testing"

	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestResolveCommandTopLevel(t *testing.T) {
	name, args, from := resolveCommand("create", []string{"myapp"})
	if name != "apps:create" || from != "create" || !reflect.DeepEqual(args, []string{"myapp"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
}

func TestResolveCommandSubcommand(t *testing.T) {
	name, args, from := resolveCommand("env", []string{"set", "FOO=bar"})
	if name != "env:set" || from != "env set" || !reflect.DeepEqual(args, []string{"FOO=bar"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = resolveCommand("alert", []string{"add", "--metric", "cpu_percent"})
	if name != "alert:add" || from != "alert add" {
		t.Fatalf("alert add got %q from=%q", name, from)
	}
}

func TestResolveCommandClusterBackupMoved(t *testing.T) {
	name, args, from := resolveCommand("cluster", []string{"backup", "--file", "x"})
	if name != "cluster:backup" || from != "cluster backup" {
		t.Fatalf("got %q from=%q", name, from)
	}
	if !reflect.DeepEqual(args, []string{"--file", "x"}) {
		t.Fatalf("args %q", args)
	}
	if movedToHost[name] != "flynn-host backup" {
		t.Fatalf("moved hint %q", movedToHost[name])
	}
	name, args, from = resolveCommand("cluster", []string{"ca"})
	if name != "cluster:ca" || from != "cluster ca" {
		t.Fatalf("cluster ca got %q from=%q", name, from)
	}
}

func TestExpandColonSuffix(t *testing.T) {
	if got := expandColonSuffixWith("redis", "cli", nil); !reflect.DeepEqual(got, []string{"redis-cli"}) {
		t.Fatalf("redis:cli %q", got)
	}
	if got := expandColonSuffixWith("kafka", "topics:create", nil); !reflect.DeepEqual(got, []string{"topics", "create"}) {
		t.Fatalf("kafka:topics:create %q", got)
	}
	if got := expandColonSuffixWith("kafka", "topics-create", nil); !reflect.DeepEqual(got, []string{"topics", "create"}) {
		t.Fatalf("kafka:topics-create alias %q", got)
	}
	if got := expandColonSuffixWith("kafka", "consumer-groups:create", nil); !reflect.DeepEqual(got, []string{"consumer-groups", "create"}) {
		t.Fatalf("kafka:consumer-groups:create %q", got)
	}
	if got := expandColonSuffixWith("redis", "dump", nil); !reflect.DeepEqual(got, []string{"dump"}) {
		t.Fatalf("redis:dump %q", got)
	}
}

// Hyphenated plugin nouns must not be split on "-" when the catalog declares
// them: flynn kafka:consumer-groups is the action "consumer-groups", not
// "consumer groups".
func TestExpandColonSuffixKeepsHyphenatedNouns(t *testing.T) {
	kafka := &plugin.CLI{Command: "kafka", Actions: []plugin.CLIAction{
		{Name: "topics"},
		{Name: "topics create"},
		{Name: "consumer-groups"},
		{Name: "consumer-groups create"},
	}}
	cases := map[string][]string{
		"consumer-groups":        {"consumer-groups"},
		"consumer-groups:create": {"consumer-groups", "create"},
		"consumer-groups-create": {"consumer-groups", "create"},
		"consumer-groups:info":   {"consumer-groups", "info"},
		"topics":                 {"topics"},
		"topics-create":          {"topics", "create"},
		"topics:create":          {"topics", "create"},
		"topics:info":            {"topics", "info"},
		"partitions-reassign":    {"partitions", "reassign"},
		"consumer-groups:x:y:z":  {"consumer-groups", "x", "y", "z"},
	}
	for suffix, want := range cases {
		if got := expandColonSuffixWith("kafka", suffix, kafka); !reflect.DeepEqual(got, want) {
			t.Errorf("kafka:%s = %q, want %q", suffix, got, want)
		}
	}
	if got := expandColonSuffixWith("kafka", "cli", kafka); !reflect.DeepEqual(got, []string{"cli"}) {
		t.Errorf("kafka:cli with catalog = %q", got)
	}
}

func TestResolveCommandLogSinkCanonical(t *testing.T) {
	name, args, from := resolveCommand("log-sink", []string{"add", "syslog", "syslog://x"})
	if name != "log-sink:add" || from != "log-sink add" || !reflect.DeepEqual(args, []string{"syslog", "syslog://x"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
}

func TestResolveCommandLogSinkCompat(t *testing.T) {
	name, args, from := resolveCommand("logsink", []string{"add", "syslog", "syslog://x"})
	if name != "log-sink:add" || from != "logsink add" || !reflect.DeepEqual(args, []string{"syslog", "syslog://x"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = resolveCommand("logsink:add", []string{"syslog", "syslog://x"})
	if name != "log-sink:add" || from != "logsink:add" || !reflect.DeepEqual(args, []string{"syslog", "syslog://x"}) {
		t.Fatalf("colon got %q %q from=%q", name, args, from)
	}
	name, args, from = resolveCommand("logsink", nil)
	if name != "log-sink" || from != "logsink" || len(args) != 0 {
		t.Fatalf("list got %q %q from=%q", name, args, from)
	}
}

func TestPluginColonName(t *testing.T) {
	if got := pluginColonName("redis", "redis-cli"); got != "redis:cli" {
		t.Fatalf("got %q", got)
	}
	if got := pluginColonName("kafka", "topics create"); got != "kafka:topics:create" {
		t.Fatalf("got %q", got)
	}
	if got := pluginColonName("kafka", "consumer-groups create"); got != "kafka:consumer-groups:create" {
		t.Fatalf("hyphenated noun got %q", got)
	}
	if got := pluginColonName("kafka", "update-all"); got != "kafka:update-all" {
		t.Fatalf("hyphenated verb got %q", got)
	}
	if got := pluginColonName("acme", "disable system routes"); got != "acme:disable-system-routes" {
		t.Fatalf("multi-word action got %q", got)
	}
	if got := pluginColonName("mysql", "console"); got != "mysql:cli" {
		t.Fatalf("got %q", got)
	}
}
