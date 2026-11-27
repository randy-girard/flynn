package authz

import (
	"net/http"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestRouteTableLookup(t *testing.T) {
	tab := NewRouteTable()
	tab.Add(http.MethodGet, "/apps/:apps_id", AppAnyGrant())
	tab.Add(http.MethodGet, "/apps/:apps_id/log", App(PermAppLogsRead))
	tab.Add(http.MethodDelete, "/domains/:hostname", UserSession())
	tab.Add(http.MethodGet, "/domains/verify", ClusterOnly())
	tab.Add(http.MethodGet, "/domains/:hostname", AnyAuth())
	tab.Add(http.MethodGet, "/ca-cert", Public("tofu"))

	req, app, ok := tab.Lookup(http.MethodGet, "/apps/demo/log/")
	if !ok || app != "demo" || req.String() != "app "+PermAppLogsRead {
		t.Fatalf("lookup = %v %q %v", req, app, ok)
	}
	// HEAD uses the GET row.
	if req, _, ok := tab.Lookup(http.MethodHead, "/apps/demo"); !ok || req.String() != "app:any-grant" {
		t.Fatalf("HEAD lookup = %v %v", req, ok)
	}
	// A static segment beats a parameter.
	if p := tab.Pattern(http.MethodGet, "/domains/verify"); p != "/domains/verify" {
		t.Fatalf("static match = %q", p)
	}
	if p := tab.Pattern(http.MethodGet, "/domains/example.com"); p != "/domains/:hostname" {
		t.Fatalf("param match = %q", p)
	}
	if _, _, ok := tab.Lookup(http.MethodPost, "/apps/demo/log"); ok {
		t.Fatal("POST has no row")
	}
	if !tab.Public(http.MethodHead, "/ca-cert") || tab.Public(http.MethodGet, "/apps/demo") {
		t.Fatal("public rows")
	}
	// No row is cluster-only.
	grant := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "demo", Permissions: []string{PermAppAdmin}}}}
	if tab.Allowed(grant, http.MethodPost, "/apps/demo/log", nil) {
		t.Fatal("an unregistered route must be cluster-only")
	}
	if !tab.Allowed(&authorizer.Token{ClusterKey: true}, http.MethodPost, "/apps/demo/log", nil) {
		t.Fatal("the cluster key reaches the router (which answers 404)")
	}
	if !tab.Allowed(grant, http.MethodGet, "/apps/demo/log", nil) {
		t.Fatal("app admin may read the log")
	}
}

func TestRouteTableRefusesUndeclaredAndDuplicateRows(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: no panic", name)
			}
		}()
		fn()
	}
	tab := NewRouteTable()
	mustPanic("zero requirement", func() { tab.Add(http.MethodGet, "/x", Requirement{}) })
	tab.Add(http.MethodGet, "/y", ClusterOnly())
	mustPanic("duplicate", func() { tab.Add(http.MethodGet, "/y", AnyAuth()) })
	mustPanic("public without reason", func() { Public(" ") })
	mustPanic("app without perm", func() { App("") })
	mustPanic("non-service scope", func() { ClusterOnly().Also("cluster:admin") })
}

func TestRequirementAlsoScopes(t *testing.T) {
	acme := &authorizer.Token{UserID: "acme", Scopes: []string{ScopeACMEIssue}}
	quota := &authorizer.Token{UserID: "billing", Scopes: []string{ScopeAccountQuota}}
	if ClusterOnly().Allows(acme, "", nil) || !ClusterOnly().Also(ScopeACMEIssue).Allows(acme, "", nil) {
		t.Fatal("acme reaches only rows that name its scope")
	}
	// A service token on a user-session row gets in only through its scope,
	// never through its caller label.
	if UserSession().Allows(acme, "", nil) || !UserSession().Also(ScopeACMEIssue).Allows(acme, "", nil) {
		t.Fatal("user-session row")
	}
	if UserSession().Also(ScopeACMEIssue).Allows(quota, "", nil) {
		t.Fatal("quota must not use the acme scope")
	}
	if (Requirement{}).Allows(&authorizer.Token{ClusterKey: true}, "", nil) {
		t.Fatal("an undeclared requirement allows no one")
	}
}
