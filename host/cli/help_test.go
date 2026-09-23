package cli

import (
	"strings"
	"testing"
)

func TestFormatHelpListsNamespaceCommands(t *testing.T) {
	got := FormatHelp("otel")
	for _, want := range []string{"usage: flynn-host otel", "Commands:", "otel:add", "otel:remove", "OpenTelemetry"} {
		if !strings.Contains(got, want) {
			t.Fatalf("otel help missing %q:\n%s", want, got)
		}
	}
	plugin := FormatHelp("plugin")
	for _, want := range []string{"Commands:", "plugin:install", "plugin:uninstall", "plugin:update", "plugin:update-all", "plugin:route", "plugin:credentials", "plugin:list"} {
		if !strings.Contains(plugin, want) {
			t.Fatalf("plugin help missing %q:\n%s", want, plugin)
		}
	}
	if strings.Contains(plugin, "credentials-set") || strings.Contains(plugin, "credentials set") {
		t.Fatalf("plugin help should list the credentials namespace, not hyphen verbs:\n%s", plugin)
	}
	if strings.Contains(plugin, "plugin:credentials:set") {
		t.Fatalf("plugin help should not list grandchildren:\n%s", plugin)
	}
	list := FormatHelp("plugin:list")
	if strings.Contains(list, "\nCommands:") {
		t.Fatalf("plugin:list is a leaf:\n%s", list)
	}
	creds := FormatHelp("plugin:credentials")
	for _, want := range []string{"plugin:credentials:set", "plugin:credentials:unset", "plugin:credentials:show"} {
		if !strings.Contains(creds, want) {
			t.Fatalf("plugin:credentials help missing %q:\n%s", want, creds)
		}
	}
	fw := FormatHelp("firewall")
	for _, want := range []string{"firewall:peer:add", "firewall:peer:remove", "firewall:expose", "firewall:sync", "firewall:unexpose"} {
		if !strings.Contains(fw, want) {
			t.Fatalf("firewall help missing %q:\n%s", want, fw)
		}
	}
	if strings.Contains(fw, "peer-add") {
		t.Fatalf("firewall help should list firewall:peer:add, not peer-add:\n%s", fw)
	}
	alert := FormatHelp("alert")
	for _, want := range []string{"usage: flynn-host alert", "Commands:", "alert:add", "alert:enable", "alert:disable", "alert:remove"} {
		if !strings.Contains(alert, want) {
			t.Fatalf("alert help missing %q:\n%s", want, alert)
		}
	}
	add := FormatHelp("otel:add")
	if strings.Contains(add, "\nCommands:") {
		t.Fatalf("otel:add should not list the namespace:\n%s", add)
	}
	if !strings.Contains(add, "--auth") {
		t.Fatalf("otel:add help missing --auth:\n%s", add)
	}
}

func TestRootHelpListsParentsOnly(t *testing.T) {
	got := RootHelp()
	for _, want := range []string{"Commands:", "plugin", "otel", "volume", "acme", "help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("root help missing %q:\n%s", want, got)
		}
	}
	for _, nested := range []string{"plugin:install", "plugin:list", "otel:add", "volume:gc", "acme:configure"} {
		if strings.Contains(got, nested) {
			t.Fatalf("root help should not list %q:\n%s", nested, got)
		}
	}
	if strings.Contains(got, "Example:") {
		t.Fatalf("root help leaked an example heading:\n%s", got)
	}
	if !strings.Contains(got, "Lists ID and IP of each host") {
		t.Fatalf("list summary missing:\n%s", got)
	}
}

func TestHelpTopicKeepsNamespaceRoots(t *testing.T) {
	if got := HelpTopic("plugin", []string{"--help"}); got != "plugin" {
		t.Fatalf("plugin --help: %q", got)
	}
	if got := HelpTopic("plugin", nil); got != "plugin" {
		t.Fatalf("plugin: %q", got)
	}
	if got := HelpTopic("plugin:install", []string{"--help"}); got != "plugin:install" {
		t.Fatalf("plugin:install --help: %q", got)
	}
	if got := HelpTopic("otel", []string{"add", "--help"}); got != "otel:add" {
		t.Fatalf("otel add --help: %q", got)
	}
}

func TestAllHostCommandsHaveHelp(t *testing.T) {
	for name, cmd := range commands {
		if strings.TrimSpace(cmd.usage) == "" {
			t.Errorf("%s has empty usage", name)
		}
		got := FormatHelp(name)
		if !strings.Contains(got, "usage: flynn-host "+name) && hyphenAliases[name] == "" {
			t.Errorf("%s help missing usage line:\n%s", name, got)
		}
	}
}

func TestWantsHelp(t *testing.T) {
	if !WantsHelp([]string{"--help"}) || !WantsHelp([]string{"-h"}) || !WantsHelp([]string{"add", "--help"}) {
		t.Fatal("expected help")
	}
	if WantsHelp(nil) || WantsHelp([]string{"add", "http://x"}) {
		t.Fatal("did not expect help")
	}
}
