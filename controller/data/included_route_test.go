package data

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

func TestIncludedHTTPRouteDomain(t *testing.T) {
	if got := IncludedHTTPRouteDomain("shop", "demo.localflynn.com"); got != "shop.demo.localflynn.com" {
		t.Fatalf("got %q", got)
	}
	if got := IncludedHTTPRouteDomain(" shop ", " demo.localflynn.com "); got != "shop.demo.localflynn.com" {
		t.Fatalf("trim: got %q", got)
	}
	if got := IncludedHTTPRouteDomain("", "demo.localflynn.com"); got != "" {
		t.Fatalf("empty app: got %q", got)
	}
}

func TestIsIncludedHTTPRoute(t *testing.T) {
	app := &ct.App{Name: "shop"}
	domain := "demo.localflynn.com"
	included := &router.Route{Type: "http", Domain: "shop.demo.localflynn.com", Path: "/"}
	if !IsIncludedHTTPRoute(included, app, domain) {
		t.Fatal("expected included route")
	}
	if !IsIncludedHTTPRoute(&router.Route{Domain: "SHOP.demo.localflynn.com"}, app, domain) {
		t.Fatal("domain match is case-insensitive and path defaults to /")
	}
	if IsIncludedHTTPRoute(&router.Route{Type: "http", Domain: "www.example.com", Path: "/"}, app, domain) {
		t.Fatal("custom domain is not included")
	}
	if IsIncludedHTTPRoute(&router.Route{Type: "http", Domain: "shop.demo.localflynn.com", Path: "/preview/"}, app, domain) {
		t.Fatal("path route is not the included hostname")
	}
	if IsIncludedHTTPRoute(&router.Route{Type: "tcp", Domain: "shop.demo.localflynn.com"}, app, domain) {
		t.Fatal("TCP is not included")
	}
	sys := &ct.App{Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}
	if IsIncludedHTTPRoute(included, sys, domain) {
		t.Fatal("system apps do not have an included user route")
	}
}

func TestMarkIncludedRoutes(t *testing.T) {
	app := &ct.App{Name: "shop"}
	routes := []*router.Route{
		{Type: "http", Domain: "shop.demo.localflynn.com"},
		{Type: "http", Domain: "www.example.com"},
	}
	MarkIncludedRoutes(app, "demo.localflynn.com", routes...)
	if !routes[0].Included {
		t.Fatal("first route should be included")
	}
	if routes[1].Included {
		t.Fatal("custom route should not be included")
	}
}
