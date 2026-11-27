package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
)

var updateAuthzGolden = flag.Bool("update-authz-golden", false, "rewrite testdata/route_authz.golden")

const routeAuthzGolden = "testdata/route_authz.golden"

type goldenPrincipal struct {
	name string
	tok  *authorizer.Token
}

func grantTok(user string, scopes []string, app string, perms ...string) *authorizer.Token {
	return &authorizer.Token{UserID: user, Scopes: scopes, AppGrants: []authorizer.AppGrant{{AppID: app, Permissions: perms}}}
}

// goldenPrincipals is the caller matrix of the route authz golden. "app-a"
// is the app the grants name. "anon" never authenticates.
func goldenPrincipals() []goldenPrincipal {
	ps := []goldenPrincipal{
		{"anon", nil},
		{"empty", &authorizer.Token{}},
		{"cluster-key", &authorizer.Token{ClusterKey: true}},
		{"cluster-admin-jwt", &authorizer.Token{UserID: "admin", Scopes: []string{"cluster:admin"}}},
		{"user", &authorizer.Token{UserID: "u1"}},
		{"user-app-admin", grantTok("u1", nil, "app-a", authz.PermAppAdmin)},
		{"grant-app-admin", grantTok("", nil, "app-a", authz.PermAppAdmin)},
		{"grant-app-write", grantTok("", nil, "app-a", authz.PermAppWrite)},
		{"grant-app-read", grantTok("", nil, "app-a", authz.PermAppRead)},
		{"grant-controller-write", grantTok("", nil, "controller", authz.PermAppWrite)},
		{"receive", grantTok("build:u1", []string{authz.ScopeGitreceiveBuild}, "app-a", authz.PermAppWrite)},
		{"build", grantTok("build:u1", []string{authz.ScopeBuildArtifacts}, "app-a", authz.PermAppWrite)},
		{"build-no-grant", &authorizer.Token{Scopes: []string{authz.ScopeBuildArtifacts}}},
		{"events", grantTok("", []string{authz.ScopeEventsRead}, "app-a", authz.ScopeEventsRead)},
		{"quota", &authorizer.Token{UserID: "billing", Scopes: []string{authz.ScopeAccountQuota}}},
		{"acme", &authorizer.Token{UserID: "acme", Scopes: []string{authz.ScopeACMEIssue}}},
	}
	for _, perm := range []string{
		authz.PermAppDeploy, authz.PermAppDelete, authz.PermAppEnvWrite,
		authz.PermAppLogsRead, authz.PermAppMetricsRead, authz.PermAppScaleRead,
		authz.PermAppScaleWrite, authz.PermAppJobsRead, authz.PermAppJobsRun,
		authz.PermAppJobsStop, authz.PermAppActivityRead, authz.PermAppRoutesRead,
		authz.PermAppRoutesWrite, authz.PermAppResourcesRead, authz.PermAppResourcesWrite,
		authz.PermAppGitHubRead, authz.PermAppGitHubWrite, authz.PermAppSchedulerRead,
		authz.PermAppSchedulerWrite, authz.PermAppTeamWrite,
	} {
		ps = append(ps, goldenPrincipal{"grant-" + strings.ReplaceAll(perm, ":", "-"), grantTok("", nil, "app-a", perm)})
	}
	return ps
}

// goldenParam is the value a route parameter takes in a probe path.
func goldenParam(name string) []string {
	switch name {
	case "apps_id":
		// app-a is granted, app-b is not, controller is a platform app,
		// and name-a is app-a by name (the resolver maps it to the id).
		return []string{"app-a", "app-b", "controller", "name-a"}
	case "routes_type":
		return []string{"http"}
	}
	return []string{"x-" + strings.ReplaceAll(name, "_", "-")}
}

func expandPattern(pattern string) []string {
	out := []string{""}
	for _, seg := range strings.Split(strings.Trim(pattern, "/"), "/") {
		vals := []string{seg}
		if strings.HasPrefix(seg, ":") {
			vals = goldenParam(seg[1:])
		}
		var next []string
		for _, prefix := range out {
			for _, v := range vals {
				next = append(next, prefix+"/"+v)
			}
		}
		out = next
	}
	return out
}

// goldenUnregistered are requests no route serves. They show what the
// authz layer answers before the router returns 404.
var goldenUnregistered = [][2]string{
	{"GET", "/"},
	{"GET", "/no-such-route"},
	{"POST", "/apps/app-a/releases"},
	{"PUT", "/apps/app-a/formation"},
	{"GET", "/apps/app-a/no-such-sub"},
	{"POST", "/apps/app-a/no-such-sub"},
	{"PATCH", "/apps/app-a/jobs/x"},
	{"PUT", "/cluster/settings"},
	{"DELETE", "/hosts/h1"},
	{"GET", "/providers/p1/resources/r1/apps"},
	{"DELETE", "/routes"},
	{"POST", "/managed-certificates"},
	{"DELETE", "/acme/config"},
	{"GET", "/apps/app-a/routes/http/r1/extra"},
	{"PATCH", "/apps/app-a/routes/http/r1"},
	{"POST", "/apps/app-a/routes/http/r1"},
	{"GET", "/accounts/a1"},
	{"GET", "/github/webhook"},
	{"GET", "/events/1/2"},
}

func goldenResolve(ref string) string {
	if ref == "name-a" {
		return "app-a"
	}
	return ""
}

// effectiveDecision is what muxHandler does with the request: serve it
// before auth (public), refuse an anonymous caller (401), or apply the
// authz decision (allow, or 403).
func effectiveDecision(tok *authorizer.Token, method, path string) string {
	if preAuthPublic(method, path) {
		return "public"
	}
	if tok == nil {
		return "401"
	}
	if routeAllowed(tok, method, path, goldenResolve) {
		return "allow"
	}
	return "403"
}

func goldenLine(principals []goldenPrincipal, method, path string) string {
	var allow []string
	pub := true
	for _, p := range principals {
		d := effectiveDecision(p.tok, method, path)
		if d != "public" {
			pub = false
		}
		if d == "allow" {
			allow = append(allow, p.name)
		}
	}
	if pub {
		return fmt.Sprintf("%s %s: public", method, path)
	}
	return fmt.Sprintf("%s %s: allow=%s", method, path, strings.Join(allow, ","))
}

func registeredTestRoutes(t *testing.T) *routeTable {
	t.Helper()
	rt := newRouteTable()
	registerRoutes(rt, &controllerAPI{}, func() error { return nil })
	return rt
}

func renderAuthzGolden(t *testing.T) string {
	principals := goldenPrincipals()
	var lines []string
	seen := map[string]bool{}
	add := func(method, path string) {
		key := method + " " + path
		if seen[key] {
			return
		}
		seen[key] = true
		lines = append(lines, goldenLine(principals, method, path))
	}
	for _, r := range registeredTestRoutes(t).routes {
		for _, path := range expandPattern(r.Pattern) {
			add(r.Method, path)
			if r.Method == http.MethodGet {
				add(http.MethodHead, path)
			}
		}
	}
	sort.Strings(lines)
	var unreg []string
	for _, req := range goldenUnregistered {
		unreg = append(unreg, goldenLine(principals, req[0], req[1]))
	}
	var b strings.Builder
	b.WriteString("# Route authz golden: who reaches each controller route.\n")
	b.WriteString("# Regenerate: go test ./controller -run TestRouteAuthzGolden -update-authz-golden\n")
	b.WriteString("# anon is 401 on every line that is not public.\n")
	b.WriteString("[registered]\n")
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n[unregistered]\n")
	b.WriteString(strings.Join(unreg, "\n"))
	b.WriteString("\n")
	return b.String()
}

// TestRouteAuthzGolden pins the authz decision of every registered route
// for a matrix of callers. A change in who may call a route shows up as a
// diff of testdata/route_authz.golden.
func TestRouteAuthzGolden(t *testing.T) {
	got := renderAuthzGolden(t)
	if *updateAuthzGolden {
		if err := os.MkdirAll(filepath.Dir(routeAuthzGolden), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(routeAuthzGolden, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(routeAuthzGolden)
	if err != nil {
		t.Fatalf("read golden (run with -update-authz-golden to create it): %s", err)
	}
	if got == string(want) {
		return
	}
	wantLines := strings.Split(string(want), "\n")
	gotLines := strings.Split(got, "\n")
	wantSet := map[string]bool{}
	for _, l := range wantLines {
		wantSet[l] = true
	}
	gotSet := map[string]bool{}
	for _, l := range gotLines {
		gotSet[l] = true
	}
	var diff []string
	for _, l := range wantLines {
		if !gotSet[l] {
			diff = append(diff, "- "+l)
		}
	}
	for _, l := range gotLines {
		if !wantSet[l] {
			diff = append(diff, "+ "+l)
		}
	}
	t.Fatalf("route authz changed (- golden, + now):\n%s", strings.Join(diff, "\n"))
}

// TestEveryRouteHasExplicitRequirement walks the routes registerRoutes adds
// to the real router. Each one declares a requirement, the router serves
// it, and the authz table resolves every concrete path of the route to
// that same row (no other row shadows it).
func TestEveryRouteHasExplicitRequirement(t *testing.T) {
	rt := registeredTestRoutes(t)
	if len(rt.routes) < 150 {
		t.Fatalf("only %d routes registered; registerRoutes did not run", len(rt.routes))
	}
	for _, r := range rt.routes {
		if !r.Req.Declared() {
			t.Errorf("%s %s has no authz requirement", r.Method, r.Pattern)
		}
		for _, path := range expandPattern(r.Pattern) {
			if h, _, _ := rt.router.Lookup(r.Method, path); h == nil {
				t.Errorf("%s %s: the router does not serve %s", r.Method, r.Pattern, path)
			}
			if got := rt.authz.Pattern(r.Method, path); got != r.Pattern {
				t.Errorf("%s %s: authz resolves to row %q", r.Method, path, got)
			}
		}
	}
	var rows int
	rt.authz.Each(func(string, string, authz.Requirement) { rows++ })
	if rows != len(rt.routes) {
		t.Fatalf("authz table has %d rows for %d routes", rows, len(rt.routes))
	}
}

// TestRoutesRegisterOnlyThroughTheTable keeps routes from bypassing the
// authz table: no controller source outside route_table.go builds an
// httprouter or adds a route to one.
func TestRoutesRegisterOnlyThroughTheTable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "route_table.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"httprouter.New(", ".router.Handle(", ".router.GET(", ".router.POST(", ".router.PUT(", ".router.PATCH(", ".router.DELETE(", "*httprouter.Router"} {
			if strings.Contains(string(src), bad) {
				t.Errorf("%s contains %q: register routes with routeTable so each declares its authz", f, bad)
			}
		}
	}
}
