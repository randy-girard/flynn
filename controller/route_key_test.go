package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

func TestPresentRouteOmitsPrivateKeyWithoutClusterAdmin(t *testing.T) {
	pem := "-----BEGIN PRIVATE KEY-----\nsecret-key-material\n-----END PRIVATE KEY-----"
	cert := "-----BEGIN CERTIFICATE-----\npublic-cert\n-----END CERTIFICATE-----"
	route := &router.Route{
		Type:   "http",
		Domain: "app.example.com",
		Certificate: &router.Certificate{
			Cert: cert,
			Key:  pem,
		},
		LegacyTLSKey: pem,
	}
	reader := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{
		AppGrants: []authorizer.AppGrant{{
			AppID:       "app-1",
			Permissions: []string{"app:routes:read", "app:routes:write"},
		}},
	})
	got := presentRoute(reader, route)
	if got.Certificate == nil || got.Certificate.Cert != cert {
		t.Fatalf("public cert = %+v", got.Certificate)
	}
	if got.Certificate.Key != "" || got.LegacyTLSKey != "" || strings.Contains(string(routeJSON(t, got)), "secret-key-material") {
		t.Fatalf("private key leaked: %+v", got)
	}
	if route.Certificate.Key != pem || route.LegacyTLSKey != pem {
		t.Fatal("presentRoute mutated the stored route")
	}

	listed := presentRoutes(reader, []*router.Route{route})
	if listed[0].Certificate.Key != "" || strings.Contains(string(routeJSON(t, listed[0])), "secret-key-material") {
		t.Fatalf("list leaked private key: %+v", listed[0].Certificate)
	}

	admin := context.WithValue(context.Background(), authz.TokenContextKey, &authorizer.Token{ClusterKey: true})
	kept := presentRoute(admin, route)
	if kept.Certificate.Key != pem || kept.LegacyTLSKey != pem {
		t.Fatal("cluster key must still receive the private key")
	}

	ev := &ct.Event{
		ObjectType: ct.EventTypeRoute,
		Data:       routeJSON(t, route),
	}
	shown := presentEvent(reader, &ct.App{ID: "app-1"}, ev)
	if strings.Contains(string(shown.Data), "secret-key-material") {
		t.Fatalf("route event leaked private key: %s", shown.Data)
	}
	if !strings.Contains(string(shown.Data), "public-cert") {
		t.Fatalf("route event dropped the public cert: %s", shown.Data)
	}
	if !strings.Contains(string(ev.Data), "secret-key-material") {
		t.Fatal("presentEvent mutated the stored event")
	}
}

func TestKeepStoredRouteKeyWhenUpdateOmitsIt(t *testing.T) {
	stored := &router.Route{Certificate: &router.Certificate{Cert: "PUBLIC", Key: "STORED-KEY"}}
	incoming := &router.Route{Certificate: &router.Certificate{Cert: "PUBLIC"}}
	keepStoredRouteKey(incoming, stored)
	if incoming.Certificate.Key != "STORED-KEY" {
		t.Fatalf("key = %q", incoming.Certificate.Key)
	}
	if stored.Certificate.Key != "STORED-KEY" {
		t.Fatal("stored route was mutated")
	}

	replaced := &router.Route{Certificate: &router.Certificate{Cert: "OTHER", Key: ""}}
	keepStoredRouteKey(replaced, stored)
	if replaced.Certificate.Key != "" {
		t.Fatal("a different certificate must still require a private key")
	}

	explicit := &router.Route{Certificate: &router.Certificate{Cert: "PUBLIC", Key: "NEW-KEY"}}
	keepStoredRouteKey(explicit, stored)
	if explicit.Certificate.Key != "NEW-KEY" {
		t.Fatalf("explicit key = %q", explicit.Certificate.Key)
	}
}

func routeJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
