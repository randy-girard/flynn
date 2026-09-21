package appliancehttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/randy-girard/flynn/pkg/status"
)

func TestKeyPrefersControllerKey(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "controller")
	t.Setenv("AUTH_KEY", "auth")
	if got := Key(); got != "controller" {
		t.Fatalf("Key()=%q", got)
	}
	t.Setenv("CONTROLLER_KEY", "")
	if got := Key(); got != "auth" {
		t.Fatalf("AUTH_KEY fallback=%q", got)
	}
	t.Setenv("AUTH_KEY", "")
	if Key() != "" {
		t.Fatal("empty env must yield empty key")
	}
}

func TestAuthorizedBearerBasicAndAuthKey(t *testing.T) {
	const key = "cluster-secret"
	if Authorized(httptest.NewRequest(http.MethodGet, "/stop", nil), key) {
		t.Fatal("missing credential")
	}
	if Authorized(httptest.NewRequest(http.MethodGet, "/stop", nil), "") {
		t.Fatal("empty configured key must fail closed")
	}

	bearer := httptest.NewRequest(http.MethodGet, "/stop", nil)
	bearer.Header.Set("Authorization", "Bearer "+key)
	if !Authorized(bearer, key) {
		t.Fatal("bearer")
	}
	wrong := httptest.NewRequest(http.MethodGet, "/stop", nil)
	wrong.Header.Set("Authorization", "Bearer nope")
	if Authorized(wrong, key) {
		t.Fatal("wrong bearer")
	}

	basic := httptest.NewRequest(http.MethodGet, "/stop", nil)
	basic.SetBasicAuth("", key)
	if !Authorized(basic, key) {
		t.Fatal("basic")
	}

	header := httptest.NewRequest(http.MethodGet, "/stop", nil)
	header.Header.Set("Auth-Key", key)
	if !Authorized(header, key) {
		t.Fatal("Auth-Key")
	}
}

func TestPublicStatusPath(t *testing.T) {
	if !PublicStatusPath(status.Path) || PublicStatusPath("/status") || PublicStatusPath("/stop") {
		t.Fatal("only /.well-known/status is public")
	}
}

func TestSetAuthMatchesHTTPClientKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/stop", nil)
	SetAuth(req, "k")
	_, pass, ok := req.BasicAuth()
	if !ok || pass != "k" {
		t.Fatalf("basic=%v %q", ok, pass)
	}
}
