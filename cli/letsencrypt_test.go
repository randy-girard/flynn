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
