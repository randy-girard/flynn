package main

import (
	"reflect"
	"testing"
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
}

func TestExpandColonSuffix(t *testing.T) {
	if got := expandColonSuffix("redis", "cli"); !reflect.DeepEqual(got, []string{"redis-cli"}) {
		t.Fatalf("redis:cli %q", got)
	}
	if got := expandColonSuffix("kafka", "topics-create"); !reflect.DeepEqual(got, []string{"topics", "create"}) {
		t.Fatalf("kafka:topics-create %q", got)
	}
	if got := expandColonSuffix("redis", "dump"); !reflect.DeepEqual(got, []string{"dump"}) {
		t.Fatalf("redis:dump %q", got)
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
	if got := pluginColonName("kafka", "topics create"); got != "kafka:topics-create" {
		t.Fatalf("got %q", got)
	}
	if got := pluginColonName("mysql", "console"); got != "mysql:cli" {
		t.Fatalf("got %q", got)
	}
}
