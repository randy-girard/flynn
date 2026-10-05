package main

import (
	"testing"

	router "github.com/randy-girard/flynn/router/types"
)

func TestAuthHTTPRouteUsesRootPathAndCert(t *testing.T) {
	ctrl := &router.Route{
		Type:      "http",
		ParentRef: "controller/abc",
		Service:   "controller",
		Domain:    "controller.1.localflynn.com",
		Path:      "/",
		Certificate: &router.Certificate{
			Cert: "CERT",
			Key:  "KEY",
		},
	}
	got := authHTTPRoute(ctrl, "auth.1.localflynn.com")
	if got.Path != "/" {
		t.Fatalf("path %q", got.Path)
	}
	if got.Domain != "auth.1.localflynn.com" || got.Service != "controller" {
		t.Fatalf("%+v", got)
	}
	if got.Certificate == nil || got.Certificate.Cert != "CERT" || got.Certificate.Key != "KEY" {
		t.Fatalf("cert %+v", got.Certificate)
	}
}

func TestAuthRouteNeedsUpdateEmptyPath(t *testing.T) {
	want := &router.Route{Path: "/", Certificate: &router.Certificate{Cert: "c", Key: "k"}}
	if !authRouteNeedsUpdate(&router.Route{Path: "", Certificate: want.Certificate}, want) {
		t.Fatal("empty path must be updated so the router binds the host")
	}
	if authRouteNeedsUpdate(&router.Route{Path: "/", Certificate: want.Certificate}, want) {
		t.Fatal("healthy route")
	}
}

func TestAuthHTTPRouteOmitsIncompleteCert(t *testing.T) {
	got := authHTTPRoute(&router.Route{
		ParentRef:   "controller/abc",
		Service:     "controller",
		Certificate: &router.Certificate{Cert: "CERT"},
	}, "auth.example")
	if got.Certificate != nil {
		t.Fatalf("incomplete cert %+v", got.Certificate)
	}
	if got.Path != "/" || got.Domain != "auth.example" {
		t.Fatalf("%+v", got)
	}
}

func TestAuthRouteNeedsUpdateNilOrMissingCert(t *testing.T) {
	want := &router.Route{Path: "/", Certificate: &router.Certificate{Cert: "c", Key: "k"}}
	if !authRouteNeedsUpdate(nil, want) {
		t.Fatal("nil existing")
	}
	if !authRouteNeedsUpdate(&router.Route{Path: "/", Certificate: nil}, want) {
		t.Fatal("missing cert")
	}
}

func TestEnsureAuthRouteNotReady(t *testing.T) {
	var api *controllerAPI
	if err := api.ensureAuthRoute(); err == nil {
		t.Fatal("nil api")
	}
	if err := (&controllerAPI{}).ensureAuthRoute(); err == nil {
		t.Fatal("missing repos")
	}
}
