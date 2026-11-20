package main

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
	router "github.com/randy-girard/flynn/router/types"
)

func TestLetsEncryptPluginInstalled(t *testing.T) {
	if letsEncryptPluginInstalled(nil) {
		t.Fatal("empty list")
	}
	if !letsEncryptPluginInstalled([]*ct.App{{Name: "letsencrypt-plugin", Meta: map[string]string{plugin.MetaPlugin: "true"}}}) {
		t.Fatal("letsencrypt-plugin app")
	}
	if !letsEncryptPluginInstalled([]*ct.App{{Name: "letsencrypt", Meta: map[string]string{plugin.MetaPlugin: "true"}}}) {
		t.Fatal("plugin app")
	}
	if !letsEncryptPluginInstalled([]*ct.App{{Name: "acme", Meta: map[string]string{"flynn-system-app": "true"}}}) {
		t.Fatal("legacy acme")
	}
	if letsEncryptPluginInstalled([]*ct.App{{Name: "letsencrypt"}}) {
		t.Fatal("name without plugin meta")
	}
}

func TestLetsEncryptCommandsParse(t *testing.T) {
	enable := parseCLI(t, []string{"letsencrypt:enable", "www.example.com"})
	if enable.String["<hostname-or-route-id>"] != "www.example.com" {
		t.Fatalf("enable: %+v", enable.String)
	}
	disable := parseCLI(t, []string{"letsencrypt:disable", "http/abc"})
	if disable.String["<hostname-or-route-id>"] != "http/abc" {
		t.Fatalf("disable: %+v", disable.String)
	}
	status := parseCLI(t, []string{"letsencrypt:status"})
	if status.String["<hostname-or-route-id>"] != "" {
		t.Fatalf("status optional: %+v", status.String)
	}
	list := parseCLI(t, []string{"letsencrypt:list"})
	if list == nil {
		t.Fatal("list")
	}
}

func TestResolveLetsEncryptSpaceAlias(t *testing.T) {
	name, args, from := resolveCommand("letsencrypt", []string{"enable", "www.example.com"})
	if name != "letsencrypt:enable" || from != "letsencrypt enable" || !strings.EqualFold(args[0], "www.example.com") {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
}

func TestFindHTTPRouteMatches(t *testing.T) {
	domain := "www.example.com"
	rt := &router.Route{Type: "http", ID: "abc", Domain: domain, ParentRef: ct.RouteParentRefPrefix + "app1"}
	matches := matchLetsEncryptHTTP([]*router.Route{rt, {Type: "tcp", ID: "x", Domain: domain}}, "www.example.com")
	if len(matches) != 1 || matches[0].ID != "abc" {
		t.Fatalf("got %+v", matches)
	}
	if got := matchLetsEncryptHTTP([]*router.Route{rt}, "http/abc"); len(got) != 1 {
		t.Fatalf("route id: %+v", got)
	}
}

func matchLetsEncryptHTTP(routes []*router.Route, target string) []*router.Route {
	var matches []*router.Route
	for _, rt := range routes {
		if rt.Type != "http" {
			continue
		}
		id := rt.Type + "/" + rt.ID
		if strings.EqualFold(rt.Domain, target) || rt.ID == target || id == target {
			matches = append(matches, rt)
		}
	}
	return matches
}

// TestLetsEncryptRoutesScopedToApp: with -a (or the git remote app) the
// commands only see that app's HTTP routes, matched by name or id.
func TestLetsEncryptRoutesScopedToApp(t *testing.T) {
	appByID := map[string]*ct.App{
		"id-a": {ID: "id-a", Name: "app-a"},
		"id-b": {ID: "id-b", Name: "app-b"},
	}
	routes := []*router.Route{
		{Type: "http", ID: "ra", Domain: "www.example.com", ParentRef: ct.RouteParentRefPrefix + "id-a"},
		{Type: "http", ID: "rb", Domain: "www.example.com", ParentRef: ct.RouteParentRefPrefix + "id-b"},
		{Type: "tcp", ID: "rt", ParentRef: ct.RouteParentRefPrefix + "id-a"},
	}
	for _, owner := range []string{"app-a", "id-a"} {
		got := letsEncryptHTTPRoutes(routes, owner, appByID)
		if len(got) != 1 || got[0].ID != "ra" {
			t.Fatalf("owner %q: got %+v", owner, got)
		}
	}
	if got := letsEncryptHTTPRoutes(routes, "", appByID); len(got) != 2 {
		t.Fatalf("no app: want both HTTP routes, got %+v", got)
	}
	if got := letsEncryptHTTPRoutes(routes, "app-c", appByID); len(got) != 0 {
		t.Fatalf("unknown app: got %+v", got)
	}

	rt, app, err := matchLetsEncryptRoute(routes, "www.example.com", "app-b", appByID)
	if err != nil || rt.ID != "rb" || app.Name != "app-b" {
		t.Fatalf("app-b: %v %+v %+v", err, rt, app)
	}
	if _, _, err := matchLetsEncryptRoute(routes, "http/rb", "app-a", appByID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("another app's route id must be not found, got %v", err)
	}
	if _, _, err := matchLetsEncryptRoute(routes, "www.example.com", "", appByID); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("no app with a shared hostname must be ambiguous, got %v", err)
	}
}
