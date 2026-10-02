package plugin

import "testing"

func TestCLIActionEffectiveScope(t *testing.T) {
	if (*CLIAction)(nil).EffectiveScope() != CLIScopeApp {
		t.Fatal("nil")
	}
	if (&CLIAction{}).EffectiveScope() != CLIScopeApp {
		t.Fatal("empty")
	}
	if (&CLIAction{Cluster: true}).EffectiveScope() != CLIScopeCluster {
		t.Fatal("legacy cluster")
	}
	if (&CLIAction{Scope: CLIScopeAccount, Cluster: true}).EffectiveScope() != CLIScopeAccount {
		t.Fatal("explicit scope wins")
	}
	if (&CLIAction{Scope: "CLUSTER"}).EffectiveScope() != CLIScopeCluster {
		t.Fatal("case")
	}
	if (&CLIAction{Scope: CLIScopeAccount}).UsesSystemApp() != true {
		t.Fatal("account uses plugin app")
	}
	if (&CLIAction{Scope: CLIScopeApp}).UsesSystemApp() {
		t.Fatal("app scope uses user app")
	}
}

func TestCLIActionNormalizeScope(t *testing.T) {
	app := &CLIAction{}
	if err := app.normalizeScope(); err != nil || app.Scope != CLIScopeApp {
		t.Fatalf("default app: %v %+v", err, app)
	}
	cluster := &CLIAction{Cluster: true}
	if err := cluster.normalizeScope(); err != nil || cluster.Scope != CLIScopeCluster {
		t.Fatalf("legacy cluster: %v %+v", err, cluster)
	}
	conflict := &CLIAction{Scope: CLIScopeApp, Cluster: true}
	if err := conflict.normalizeScope(); err == nil {
		t.Fatal("cluster:true vs scope app")
	}
	bad := &CLIAction{Scope: "host"}
	if err := bad.normalizeScope(); err == nil {
		t.Fatal("unknown scope")
	}
}

func TestCLIHasScopeHelpers(t *testing.T) {
	c := &CLI{Command: "pipeline", Actions: []CLIAction{
		{Name: "create", Scope: CLIScopeAccount},
		{Name: "promote", Scope: CLIScopeApp},
	}}
	if !c.HasFlynnVisibleActions() || c.HasClusterActions() {
		t.Fatal("pipeline")
	}
	b := &CLI{Command: "billing", Actions: []CLIAction{
		{Name: "plans", Scope: CLIScopeCluster},
	}}
	if b.HasFlynnVisibleActions() || !b.HasClusterActions() {
		t.Fatal("billing")
	}
	if (*CLI)(nil).HasFlynnVisibleActions() || (*CLI)(nil).HasClusterActions() {
		t.Fatal("nil")
	}
}

func TestCLIRedirects(t *testing.T) {
	ent := &CLI{Command: "enterprise"}
	if got := ent.HostRedirect(&CLIAction{Name: "license"}); got != "flynn-host enterprise license" {
		t.Fatalf("host license: %q", got)
	}
	if got := ent.HostRedirect(&CLIAction{Name: "show"}); got != "flynn-host enterprise" {
		t.Fatalf("host show: %q", got)
	}
	if got := ent.FlynnRedirect(&CLIAction{Name: "org list"}); got != "flynn enterprise:org:list" {
		t.Fatalf("flynn org list: %q", got)
	}
	if got := (*CLI)(nil).HostRedirect(nil); got != "flynn-host" {
		t.Fatalf("nil: %q", got)
	}
}

func TestCLIActionByName(t *testing.T) {
	c := &CLI{Command: "enterprise", Actions: []CLIAction{
		{Name: "org list"},
		{Name: "license"},
	}}
	if c.ActionByName("org list") == nil || c.ActionByName("license") == nil {
		t.Fatal("direct")
	}
	if (*CLI)(nil).ActionByName("license") != nil {
		t.Fatal("nil")
	}
}
