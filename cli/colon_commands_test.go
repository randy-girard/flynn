package main

import (
	"strings"
	"testing"
)

func TestCLICommandNamesHaveNoSpaces(t *testing.T) {
	for name := range commands {
		if strings.Contains(name, " ") {
			t.Errorf("command %q uses a space; nested verbs must use a colon", name)
		}
	}
}

func TestCLINestedCommandsAreRegistered(t *testing.T) {
	want := []string{
		"alert", "alert:add", "alert:enable", "alert:disable", "alert:remove",
		"metrics",
		"env:get", "env:set", "env:unset",
		"plugin:list",
		"log-sink", "log-sink:add", "log-sink:remove",
		"apps:create", "apps:destroy",
		"docker:push", "docker:set-push-url",
		"volume:show", "volume:decommission",
		"route:add", "resource:add", "resource:expose", "resource:unexpose",
		"letsencrypt", "letsencrypt:enable", "letsencrypt:disable", "letsencrypt:status",
		"cluster:add", "cluster:migrate-domain", "cluster:ca",
		"limit:set", "limit:profiles", "limit:runtime",
		"update",
	}
	for _, name := range want {
		if commands[name] == nil {
			t.Errorf("missing registered command %s", name)
		}
	}
	if commands["upgrade"] != nil {
		t.Error("upgrade must not be a root command; keep flynn update only")
	}
}

func TestCLIHyphenatedVerbsStayHyphenated(t *testing.T) {
	for _, name := range []string{"docker:set-push-url", "cluster:migrate-domain", "log-sink:add"} {
		if commands[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{"docker:set:push:url", "cluster:migrate:domain", "log:sink:add"} {
		if commands[name] != nil {
			t.Errorf("%s must not be registered; keep the hyphenated form", name)
		}
	}
}

func TestPluginNestedColonCommands(t *testing.T) {
	if got := pluginColonName("kafka", "topics create"); got != "kafka:topics:create" {
		t.Fatalf("nested noun got %q", got)
	}
	if got := pluginColonName("kafka", "consumer-groups create"); got != "kafka:consumer-groups:create" {
		t.Fatalf("hyphenated noun got %q", got)
	}
	if got := pluginColonName("clickhouse", "databases create"); got != "clickhouse:databases:create" {
		t.Fatalf("databases create got %q", got)
	}
	if got := pluginColonName("kafka", "update-all"); got != "kafka:update-all" {
		t.Fatalf("hyphenated verb got %q", got)
	}
}
