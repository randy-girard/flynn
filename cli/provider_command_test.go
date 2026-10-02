package main

import (
	"strings"
	"testing"
)

func TestUserCLIDoesNotRegisterProvider(t *testing.T) {
	for _, name := range []string{"provider", "provider:add"} {
		if commands[name] != nil {
			t.Fatalf("%s is not a flynn command; plugins and bootstrap register providers, flynn resource attaches them", name)
		}
	}
	if _, ok := subAliases["provider"]; ok {
		t.Fatal("flynn provider space alias must not resolve to a built-in command")
	}
	name, args, from := resolveCommand("provider", []string{"add", "redis", "http://redis-plugin.discoverd/clusters"})
	if name != "provider" || from != "" {
		t.Fatalf("provider add must not rewrite, got %q %q from=%q", name, args, from)
	}
	if strings.Contains(formatCoreRootHelp(), "\n  provider") {
		t.Fatal("root help must not list provider")
	}
}
