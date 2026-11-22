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

// TestKeysSplitsControllerAndAuthKey: the controller keeps AUTH_KEY as a
// comma-separated list so keys can rotate. Every entry is accepted, and the
// first one is what a client sends.
func TestKeysSplitsControllerAndAuthKey(t *testing.T) {
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", " new , old ,")
	if got := Keys(); len(got) != 2 || got[0] != "new" || got[1] != "old" {
		t.Fatalf("Keys()=%q", got)
	}
	if got := ClientKey(); got != "new" {
		t.Fatalf("ClientKey()=%q", got)
	}
	t.Setenv("CONTROLLER_KEY", "ck")
	if got := Keys(); len(got) != 3 || got[0] != "ck" {
		t.Fatalf("Keys() with CONTROLLER_KEY=%q", got)
	}
	if got := ClientKey(); got != "ck" {
		t.Fatalf("ClientKey() prefers CONTROLLER_KEY, got %q", got)
	}
	t.Setenv("CONTROLLER_KEY", "")
	t.Setenv("AUTH_KEY", "")
	if Keys() != nil || ClientKey() != "" {
		t.Fatal("empty env must yield no keys")
	}
}

func TestRequireKeysGuardsEverythingButStatus(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := RequireKeys([]string{"a", "b"}, ok)
	do := func(path, key string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			req.SetBasicAuth("", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do(status.Path, ""); c != http.StatusOK {
		t.Fatalf("status path without key = %d", c)
	}
	for _, p := range []string{"/x", "/debug/pprof/"} {
		if c := do(p, ""); c != http.StatusUnauthorized {
			t.Fatalf("%s without key = %d", p, c)
		}
		if c := do(p, "nope"); c != http.StatusUnauthorized {
			t.Fatalf("%s with wrong key = %d", p, c)
		}
		if c := do(p, "b"); c != http.StatusOK {
			t.Fatalf("%s with second key = %d", p, c)
		}
	}
	closed := RequireKeys(nil, ok)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetBasicAuth("", "")
	closed.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no configured key must fail closed, got %d", rec.Code)
	}
}
