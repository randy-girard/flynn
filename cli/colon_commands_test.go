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
		"docker:push",
		"volume:show", "volume:decommission",
		"route:add", "resource:add", "resource:expose", "resource:unexpose",
		"cluster:add",
		"limit:set", "limit:profile", "limit:profiles",
	}
	for _, name := range want {
		if commands[name] == nil {
			t.Errorf("missing registered command %s", name)
		}
	}
}
