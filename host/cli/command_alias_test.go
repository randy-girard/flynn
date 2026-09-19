package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveCommandVolumeSpaceAlias(t *testing.T) {
	name, args, from := ResolveCommand("volume", []string{"gc"})
	if name != "volume:gc" || from != "volume gc" || len(args) != 0 {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("volume", []string{"delete", "vol-1"})
	if name != "volume:delete" || from != "volume delete" || !reflect.DeepEqual(args, []string{"vol-1"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("volume", nil)
	if name != "volume:list" || from != "volume" || len(args) != 0 {
		t.Fatalf("bare volume got %q %q from=%q", name, args, from)
	}
}

func TestResolveCommandPluginSpaceAlias(t *testing.T) {
	name, args, from := ResolveCommand("plugin", []string{"install", "redis", "--ref", "v1"})
	if name != "plugin:install" || from != "plugin install" || !reflect.DeepEqual(args, []string{"redis", "--ref", "v1"}) {
		t.Fatalf("install got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("plugin", []string{"credentials", "set", "github"})
	if name != "plugin:credentials-set" || from != "plugin credentials set" || !reflect.DeepEqual(args, []string{"github"}) {
		t.Fatalf("credentials got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("plugin", []string{"dashboard", "route", "add", "http", "--auto-tls"})
	if name != "plugin:route" || from != "plugin dashboard route" || !reflect.DeepEqual(args, []string{"dashboard", "add", "http", "--auto-tls"}) {
		t.Fatalf("route got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("plugin", []string{"credentials", "--help"})
	if name != "plugin:credentials" || from != "plugin credentials" || !reflect.DeepEqual(args, []string{"--help"}) {
		t.Fatalf("credentials help got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("plugin", []string{"update-all"})
	if name != "plugin:update-all" || from != "plugin update-all" || len(args) != 0 {
		t.Fatalf("update-all got %q %q from=%q args=%q", name, args, from, args)
	}
	name, args, from = ResolveCommand("plugin", []string{"--help"})
	if name != "plugin:list" || from != "plugin" || !reflect.DeepEqual(args, []string{"--help"}) {
		t.Fatalf("plugin help got %q %q from=%q", name, args, from)
	}
}

func TestResolveCommandLogSinkAlias(t *testing.T) {
	name, args, from := ResolveCommand("logsink", []string{"add", "syslog", "syslog://x"})
	if name != "log-sink:add" || from != "logsink add" || !reflect.DeepEqual(args, []string{"syslog", "syslog://x"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("log-sink", []string{"add", "syslog", "syslog://x"})
	if name != "log-sink:add" || from != "log-sink add" || !reflect.DeepEqual(args, []string{"syslog", "syslog://x"}) {
		t.Fatalf("canonical space got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("log-sink:add", []string{"syslog", "syslog://x"})
	if name != "log-sink:add" || from != "" || !reflect.DeepEqual(args, []string{"syslog", "syslog://x"}) {
		t.Fatalf("colon got %q %q from=%q", name, args, from)
	}
}

func TestHostCommandNamesHaveNoSpaces(t *testing.T) {
	for name := range commands {
		if strings.Contains(name, " ") {
			t.Errorf("command %q uses a space; nested verbs must use a colon", name)
		}
	}
}

func TestHostNestedCommandsAreRegistered(t *testing.T) {
	want := []string{
		"volume:list", "volume:create", "volume:delete", "volume:gc",
		"log-sink", "log-sink:add", "log-sink:list", "log-sink:remove",
		"otel", "otel:add", "otel:remove",
		"plugin:install", "plugin:list", "plugin:update", "plugin:update-all", "plugin:uninstall",
		"plugin:credentials", "plugin:credentials-set", "plugin:credentials-unset", "plugin:credentials-show",
		"plugin:credentials-set", "plugin:credentials-unset", "plugin:credentials-show",
		"plugin:route",
		"tags", "tags:set", "tags:del",
		"webhooks", "webhooks:add", "webhooks:remove",
		"acme", "acme:configure", "acme:enable", "acme:disable", "acme:status",
		"acme:enable-system-routes", "acme:disable-system-routes",
		"github", "github:configure", "github:status", "github:setup", "github:disable",
		"domain", "domain:apex",
		"runtime-profile", "runtime-profile:create", "runtime-profile:update",
		"runtime-profile:remove", "runtime-profile:allow-custom",
		"events", "events:visible",
		"route:add",
		"firewall", "firewall:sync", "firewall:peer-add", "firewall:peer-remove",
		"firewall:expose", "firewall:unexpose",
	}
	for _, name := range want {
		if commands[name] == nil {
			t.Errorf("missing registered command %s", name)
		}
	}
}

func TestResolveCommandRuntimeProfileAlias(t *testing.T) {
	name, args, from := ResolveCommand("runtime-profile", []string{"create", "xlarge"})
	if name != "runtime-profile:create" || from != "runtime-profile create" || !reflect.DeepEqual(args, []string{"xlarge"}) {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = ResolveCommand("route", []string{"add", "http", "--app", "admin", "example.com/admin"})
	if name != "route:add" || from != "route add" {
		t.Fatalf("route add got %q from=%q args=%q", name, from, args)
	}
	name, args, from = ResolveCommand("firewall", []string{"peer-add", "10.0.0.5"})
	if name != "firewall:peer-add" || from != "firewall peer-add" || strings.Join(args, " ") != "10.0.0.5" {
		t.Fatalf("firewall peer-add got %q from=%q args=%q", name, from, args)
	}
	name, args, from = ResolveCommand("firewall", []string{"expose", "3001"})
	if name != "firewall:expose" || from != "firewall expose" {
		t.Fatalf("firewall expose got %q from=%q", name, from)
	}
	name, args, from = ResolveCommand("alert", []string{"add", "--metric", "disk_percent"})
	if name != "alert:add" || from != "alert add" {
		t.Fatalf("alert add got %q from=%q", name, from)
	}
}

func TestHostCommandUsageStartsWithColonName(t *testing.T) {
	for name, cmd := range commands {
		line := firstUsageLine(cmd.usage)
		prefix := "usage: flynn-host " + name
		if !strings.HasPrefix(line, prefix) {
			t.Errorf("%s usage starts %q, want prefix %q", name, line, prefix)
		}
	}
}

func firstUsageLine(usage string) string {
	for _, line := range strings.Split(usage, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "usage:") {
			return line
		}
	}
	return ""
}
