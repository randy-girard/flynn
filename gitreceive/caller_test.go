package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The lookup forwards only the caller's own Authorization header to
// GET /whoami/grants and maps the controller's refusals.
func TestControllerCallersForwardsCredential(t *testing.T) {
	var gotAuth, gotPath, gotCookie string
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotCookie = r.Header.Get("Cookie")
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"user_id":"u1","cluster_admin":false,"app_grants":[{"app_id":"a1","permissions":["app:deploy"]}]}`))
	}))
	defer srv.Close()
	c := &controllerCallers{url: srv.URL, client: srv.Client()}

	req := httptest.NewRequest("GET", "/mine.git/info/refs", nil)
	req.SetBasicAuth("Bearer", "tenant-token")
	req.Header.Set("Cookie", "session=x")
	g, err := c.CallerGrants(req)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/whoami/grants" {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != req.Header.Get("Authorization") {
		t.Fatalf("Authorization not forwarded as sent: %q", gotAuth)
	}
	if gotCookie != "" {
		t.Fatal("only the Authorization header may be forwarded")
	}
	if g.UserID != "u1" || len(g.AppGrants) != 1 || g.AppGrants[0].AppID != "a1" {
		t.Fatalf("grants=%+v", g)
	}

	for code, want := range map[int]error{401: errCallerUnauthorized, 403: errCallerForbidden} {
		status = code
		if _, err := c.CallerGrants(req); !errors.Is(err, want) {
			t.Fatalf("status %d: err=%v, want %v", code, err, want)
		}
	}
	status = 500
	if _, err := c.CallerGrants(req); err == nil || errors.Is(err, errCallerUnauthorized) || errors.Is(err, errCallerForbidden) {
		t.Fatalf("status 500: err=%v", err)
	}

	// No credential: nothing to ask.
	if _, err := c.CallerGrants(httptest.NewRequest("GET", "/", nil)); !errors.Is(err, errCallerUnauthorized) {
		t.Fatalf("no credential err=%v", err)
	}
}
