package clihelp

import (
	"reflect"
	"strings"
	"testing"
)

func TestParents(t *testing.T) {
	got := Parents([]string{"env:set", "env", "plugin:list", "plugin:credentials:set", "ps", "log-sink:add"})
	want := []string{"env", "log-sink", "plugin", "ps"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestChildrenSkipsGrandchildren(t *testing.T) {
	all := []string{
		"plugin:list", "plugin:install", "plugin:credentials",
		"plugin:credentials:set", "plugin:credentials:unset",
		"firewall:expose", "firewall:peer:add",
	}
	plugin := Children(all, "plugin")
	want := []string{"credentials", "install", "list"}
	if !reflect.DeepEqual(plugin, want) {
		t.Fatalf("plugin children %q want %q", plugin, want)
	}
	creds := Children(all, "plugin:credentials")
	if !reflect.DeepEqual(creds, []string{"set", "unset"}) {
		t.Fatalf("credentials children %q", creds)
	}
	fw := Children(all, "firewall")
	if !reflect.DeepEqual(fw, []string{"expose", "peer:add"}) {
		t.Fatalf("firewall children %q", fw)
	}
	if len(Children(all, "plugin:list")) != 0 {
		t.Fatalf("leaf should have no children")
	}
}

func TestHasChildren(t *testing.T) {
	all := []string{"env", "env:set", "ps"}
	if !HasChildren(all, "env") || HasChildren(all, "ps") || HasChildren(all, "missing") {
		t.Fatal("HasChildren")
	}
}

func TestStripHelp(t *testing.T) {
	got := StripHelp([]string{"set", "--help", "FOO=bar"})
	if !reflect.DeepEqual(got, []string{"set", "FOO=bar"}) {
		t.Fatalf("got %q", got)
	}
	got = StripHelp([]string{"--", "--help"})
	if !reflect.DeepEqual(got, []string{"--", "--help"}) {
		t.Fatalf("-- stop: %q", got)
	}
}

func TestShortDescription(t *testing.T) {
	got := ShortDescription("usage: flynn env:set\n\nSet app environment variables.\n\nOptions:\n")
	if got != "Set app environment variables" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatItems(t *testing.T) {
	got := FormatItems([]Item{{Name: "set", Desc: "Set env"}, {Name: "unset", Desc: "Unset env"}})
	for _, want := range []string{"set", "Set env", "unset"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}
