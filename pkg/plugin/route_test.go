package plugin

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
)

func pluginApp(name string) *ct.App {
	return &ct.App{ID: name + "-id", Name: name, Meta: map[string]string{MetaPlugin: "true"}}
}

func TestLookupPluginApp(t *testing.T) {
	widget := pluginApp("widget")
	cliApp := &ct.App{
		ID:   "dash-id",
		Name: "flynn-dashboard",
		Meta: map[string]string{
			MetaPlugin:    "true",
			MetaPluginCLI: `{"command":"dashboard"}`,
		},
	}
	plain := &ct.App{Name: "controller"}
	apps := []*ct.App{widget, cliApp, plain}

	got, err := LookupPluginApp(apps, "widget")
	if err != nil || got != widget {
		t.Fatalf("by name: %v %v", got, err)
	}
	got, err = LookupPluginApp(apps, "dashboard")
	if err != nil || got != cliApp {
		t.Fatalf("by CLI command: %v %v", got, err)
	}
	if _, err := LookupPluginApp(apps, "controller"); err == nil || !strings.Contains(err.Error(), "not an installed plugin") {
		t.Fatalf("non-plugin: %v", err)
	}
	if _, err := LookupPluginApp(apps, "missing"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := LookupPluginApp(apps, ""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty: %v", err)
	}
}

func TestDefaultRouteService(t *testing.T) {
	app := pluginApp("dashboard")
	if got := DefaultRouteService(app, nil, "http"); got != "dashboard" {
		t.Fatalf("app name: %q", got)
	}
	routes := []*router.Route{{Type: "http", Domain: "dashboard.ex", Service: "dashboard"}}
	if got := DefaultRouteService(app, routes, "http"); got != "dashboard" {
		t.Fatalf("from routes: %q", got)
	}
}

func TestPluginRouterAddHTTPAutoTLS(t *testing.T) {
	app := pluginApp("widget")
	stub := &routeStub{acme: &ct.ACMEConfig{Enabled: true}}
	rt := &PluginRouter{Client: stub}
	route, err := rt.AddHTTP(app, HTTPRouteOptions{
		Domain:        "widget.example",
		Service:       "widget",
		AutoTLS:       true,
		DrainBackends: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if route == nil || route.Domain != "widget.example" || !routeHasAutoTLS(route) {
		t.Fatalf("route %+v", route)
	}
	if len(stub.created) != 1 {
		t.Fatalf("created=%d", len(stub.created))
	}
}

func TestPluginRouterAddHTTPRequiresACME(t *testing.T) {
	app := pluginApp("widget")
	rt := &PluginRouter{Client: &routeStub{}}
	_, err := rt.AddHTTP(app, HTTPRouteOptions{Domain: "widget.example", AutoTLS: true, DrainBackends: true})
	if err == nil || !strings.Contains(err.Error(), "ACME") {
		t.Fatalf("got %v", err)
	}
}

func TestPluginRouterAddHTTPOmitsDomainUsesExisting(t *testing.T) {
	app := pluginApp("dashboard")
	existing := &router.Route{Type: "http", ID: "r1", Domain: "dashboard.ex.local", Service: "dashboard"}
	stub := &routeStub{
		routes: []*router.Route{existing},
		acme:   &ct.ACMEConfig{Enabled: true},
	}
	rt := &PluginRouter{Client: stub}
	route, err := rt.AddHTTP(app, HTTPRouteOptions{AutoTLS: true, DrainBackends: true})
	if err != nil {
		t.Fatal(err)
	}
	if route.Domain != "dashboard.ex.local" || !routeHasAutoTLS(route) {
		t.Fatalf("route %+v", route)
	}
	if len(stub.created) != 0 || len(stub.updated) != 1 {
		t.Fatalf("created=%d updated=%d", len(stub.created), len(stub.updated))
	}
}

func TestPluginRouterAddHTTPOmitsDomainAmbiguous(t *testing.T) {
	app := pluginApp("widget")
	stub := &routeStub{routes: []*router.Route{
		{Type: "http", ID: "a", Domain: "a.example", Service: "widget"},
		{Type: "http", ID: "b", Domain: "b.example", Service: "widget"},
	}}
	_, err := (&PluginRouter{Client: stub}).AddHTTP(app, HTTPRouteOptions{AutoTLS: true})
	if err == nil || !strings.Contains(err.Error(), "domain is required") {
		t.Fatalf("got %v", err)
	}
}

func TestPluginRouterUpdateAndRemove(t *testing.T) {
	app := pluginApp("widget")
	existing := &router.Route{Type: "http", ID: "abc", Domain: "widget.example", Service: "widget"}
	stub := &routeStub{
		routes: []*router.Route{existing},
		acme:   &ct.ACMEConfig{Enabled: true},
	}
	rt := &PluginRouter{Client: stub}
	updated, err := rt.UpdateHTTP(app, "http/abc", HTTPRouteUpdate{AutoTLS: true})
	if err != nil {
		t.Fatal(err)
	}
	if !routeHasAutoTLS(updated) {
		t.Fatal("expected auto tls")
	}
	if err := rt.Remove(app, "http/abc"); err != nil {
		t.Fatal(err)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != "http/abc" {
		t.Fatalf("deleted=%v", stub.deleted)
	}
}

func TestPluginRouterAddTCPTLS(t *testing.T) {
	app := pluginApp("redis")
	stub := &routeStub{acme: &ct.ACMEConfig{Enabled: true}}
	rt := &PluginRouter{Client: stub}
	route, err := rt.AddTCP(app, TCPRouteOptions{
		Service:       "redis",
		Leader:        true,
		DrainBackends: true,
		Domain:        "redis.example.com",
		TLSMode:       "passthrough",
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.TLSMode != router.TLSModePassthrough || route.Domain != "redis.example.com" || !route.Leader {
		t.Fatalf("%+v", route)
	}
	term, err := rt.AddTCP(app, TCPRouteOptions{
		Service: "redis",
		Domain:  "redis.example.com",
		AutoTLS: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if term.TLSMode != router.TLSModeTerminate || !routeHasAutoTLS(term) {
		t.Fatalf("terminate auto-tls %+v", term)
	}
}

func TestPluginRouterRejectsCertWithAutoTLS(t *testing.T) {
	app := pluginApp("widget")
	rt := &PluginRouter{Client: &routeStub{acme: &ct.ACMEConfig{Enabled: true}}}
	_, err := rt.AddHTTP(app, HTTPRouteOptions{Domain: "x.example", AutoTLS: true, TLSCert: "c", TLSKey: "k"})
	if err == nil || !strings.Contains(err.Error(), "cannot be used") {
		t.Fatalf("got %v", err)
	}
}
