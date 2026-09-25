package plugin

import (
	"strings"
	"testing"
)

func TestHostedGroupMembership(t *testing.T) {
	g, ok := LookupGroup("hosted")
	if !ok {
		t.Fatal("hosted plugin group missing")
	}
	if g.TenancyMode != "hosted" {
		t.Fatalf("tenancy mode %q", g.TenancyMode)
	}
	got := strings.Join(g.Plugins, ",")
	if got != "dashboard,enterprise,billing" {
		t.Fatalf("members %s", got)
	}
	if GroupTenancyMode("hosted") != "hosted" {
		t.Fatal("GroupTenancyMode")
	}
	if _, ok := LookupGroup("nope"); ok {
		t.Fatal("unknown group")
	}
}

func TestHostedGroupBillingUnresolved(t *testing.T) {
	t.Setenv(EnvGitHubOrg, "")
	t.Setenv(EnvFlynnRepo, "")
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	t.Setenv(EnvInstalledFile, t.TempDir()+"/none.json")

	_, _, err := ResolveGroupMembers("hosted", InstallOptions{})
	if err == nil {
		t.Fatal("expected billing resolve failure")
	}
	msg := err.Error()
	for _, needle := range []string{"billing", "plugins.json", "plugin:credentials"} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("error %q missing %q", msg, needle)
		}
	}
}

func TestHostedGroupResolvesBillingFromPluginsJSON(t *testing.T) {
	t.Setenv(EnvGitHubOrg, "")
	t.Setenv(EnvFlynnRepo, "")
	t.Setenv(EnvPluginRepoRoot, t.TempDir())
	t.Setenv(EnvInstalledFile, t.TempDir()+"/none.json")

	path := t.TempDir() + "/plugins.json"
	writeJSON(t, path, map[string]interface{}{
		"billing": map[string]string{"url": "https://github.com/randy-girard/flynn-plugin-billing.git"},
	})
	g, resolved, err := ResolveGroupMembers("hosted", InstallOptions{PluginsFile: path})
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "hosted" || len(resolved) != 3 {
		t.Fatalf("group %+v resolved %d", g, len(resolved))
	}
	var billing *Resolved
	for _, r := range resolved {
		if r.Input == "billing" {
			billing = r
		}
	}
	if billing == nil || billing.GitHub == nil || billing.GitHub.Repo != "flynn-plugin-billing" {
		t.Fatalf("billing resolver: %+v", billing)
	}
}
