package cli

import (
	"strings"
	"testing"
)

func TestFormatHelpListsNamespaceCommands(t *testing.T) {
	got := FormatHelp("otel")
	for _, want := range []string{"usage: flynn-host otel", "Commands:", "add", "remove", "OpenTelemetry"} {
		if !strings.Contains(got, want) {
			t.Fatalf("otel help missing %q:\n%s", want, got)
		}
	}
	plugin := FormatHelp("plugin:list")
	for _, want := range []string{"Commands:", "install", "uninstall", "update", "update-all", "route", "credentials"} {
		if !strings.Contains(plugin, want) {
			t.Fatalf("plugin help missing %q:\n%s", want, plugin)
		}
	}
	if strings.Contains(plugin, "credentials-set") || strings.Contains(plugin, "credentials set") {
		t.Fatalf("plugin help should list the credentials namespace, not hyphen verbs:\n%s", plugin)
	}
	creds := FormatHelp("plugin:credentials")
	for _, want := range []string{"set", "unset", "show"} {
		if !strings.Contains(creds, want) {
			t.Fatalf("plugin:credentials help missing %q:\n%s", want, creds)
		}
	}
	fw := FormatHelp("firewall")
	for _, want := range []string{"peer:add", "peer:remove", "expose", "sync", "unexpose"} {
		if !strings.Contains(fw, want) {
			t.Fatalf("firewall help missing %q:\n%s", want, fw)
		}
	}
	if strings.Contains(fw, "peer-add") {
		t.Fatalf("firewall help should list peer:add, not peer-add:\n%s", fw)
	}
	alert := FormatHelp("alert")
	for _, want := range []string{"usage: flynn-host alert", "Commands:", "add", "enable", "disable", "remove"} {
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

func TestAllHostCommandsHaveHelp(t *testing.T) {
	for name, cmd := range commands {
		if strings.TrimSpace(cmd.usage) == "" {
			t.Errorf("%s has empty usage", name)
		}
		got := FormatHelp(name)
		if !strings.Contains(got, "usage: flynn-host "+name) {
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
