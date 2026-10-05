package main

import (
	"net/http"
	"testing"

	cfg "github.com/randy-girard/flynn/cli/config"
)

func TestIsControllerAuthKey(t *testing.T) {
	t.Parallel()
	if !isControllerAuthKey("31783ed8e05d26555b7da633c4d4e43d") {
		t.Fatal("32-char hex cluster AUTH_KEY")
	}
	if isControllerAuthKey("flynn_pat_abc") {
		t.Fatal("PAT is not a cluster AUTH_KEY")
	}
	if isControllerAuthKey("CnIKCWZseW5uLWNsaRIkNzI1NzY5OGEtMDBjMS00") {
		t.Fatal("OAuth access token is not a cluster AUTH_KEY")
	}
	if isControllerAuthKey("") {
		t.Fatal("empty")
	}
}

func TestSetDashboardAuthUsesControllerKey(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest(http.MethodGet, "https://dashboard.example/api/alerts", nil)
	key := "31783ed8e05d26555b7da633c4d4e43d"
	setDashboardAuth(req, key)
	if got := req.Header.Get("X-Controller-Key"); got != key {
		t.Fatalf("X-Controller-Key=%q", got)
	}
	u, p, ok := req.BasicAuth()
	if !ok || u != "" || p != key {
		t.Fatalf("basic auth user=%q pass=%q ok=%v", u, p, ok)
	}
	if req.Header.Get("Authorization") == "Bearer "+key {
		t.Fatal("cluster AUTH_KEY must not be sent as Bearer")
	}
}

func TestSetDashboardAuthBearerForOAuth(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest(http.MethodGet, "https://dashboard.example/api/alerts", nil)
	tok := "CnIKCWZseW5uLWNsaRIkNzI1NzY5OGEtMDBjMS00"
	setDashboardAuth(req, tok)
	if req.Header.Get("Authorization") != "Bearer "+tok {
		t.Fatalf("Authorization=%q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("X-Controller-Key") != "" {
		t.Fatal("OAuth token must not set X-Controller-Key")
	}
}

func TestDashboardCredentialKeepsClusterKey(t *testing.T) {
	t.Parallel()
	c := &cfg.Cluster{Name: "local", Key: "31783ed8e05d26555b7da633c4d4e43d"}
	got, err := dashboardCredential(c)
	if err != nil {
		t.Fatal(err)
	}
	if got != c.Key {
		t.Fatalf("got %q", got)
	}
}
