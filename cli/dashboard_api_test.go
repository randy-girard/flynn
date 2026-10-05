package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestDashboardCredentialKeepsPAT(t *testing.T) {
	t.Parallel()
	got, err := dashboardCredential(&cfg.Cluster{Name: "local", Key: "flynn_pat_abc"})
	if err != nil || got != "flynn_pat_abc" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestDashboardCredentialRequiresLogin(t *testing.T) {
	t.Parallel()
	_, err := dashboardCredential(nil)
	if err == nil || !strings.Contains(err.Error(), "no cluster") {
		t.Fatalf("%v", err)
	}
	_, err = dashboardCredential(&cfg.Cluster{Name: "local"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("%v", err)
	}
}

func TestSetDashboardAuthPAT(t *testing.T) {
	t.Parallel()
	req, _ := http.NewRequest(http.MethodGet, "https://dashboard.example/api/alerts", nil)
	setDashboardAuth(req, "flynn_pat_secret")
	u, p, ok := req.BasicAuth()
	if !ok || u != "" || p != "flynn_pat_secret" {
		t.Fatalf("basic auth user=%q pass=%q ok=%v", u, p, ok)
	}
	if req.Header.Get("X-Controller-Key") != "" {
		t.Fatal("PAT must not set X-Controller-Key")
	}
}

func TestSetDashboardAuthNilRequest(t *testing.T) {
	t.Parallel()
	setDashboardAuth(nil, "31783ed8e05d26555b7da633c4d4e43d")
}

func TestIsControllerAuthKeyBounds(t *testing.T) {
	t.Parallel()
	if isControllerAuthKey(strings.Repeat("a", 15)) || isControllerAuthKey(strings.Repeat("a", 65)) {
		t.Fatal("length bounds")
	}
	if isControllerAuthKey("31783ed8e05d26555b7da633c4d4e43g") {
		t.Fatal("non-hex")
	}
}

func TestDashboardBaseURLFallbacks(t *testing.T) {
	t.Parallel()
	if _, err := dashboardBaseURL(nil); err == nil {
		t.Fatal("nil cluster")
	}
	got, err := dashboardBaseURL(&cfg.Cluster{OAuthURL: "https://auth.example/"})
	if err != nil || got != "https://auth.example" {
		t.Fatalf("oauth %q %v", got, err)
	}
	got, err = dashboardBaseURL(&cfg.Cluster{ControllerURL: "https://controller.demo.localflynn.com"})
	if err != nil || got != "https://dashboard.demo.localflynn.com" {
		t.Fatalf("derived %q %v", got, err)
	}
	if _, err := dashboardBaseURL(&cfg.Cluster{ControllerURL: "https://example.com"}); err == nil {
		t.Fatal("unrelated host")
	}
}

func TestDashboardTLSServerName(t *testing.T) {
	t.Parallel()
	if dashboardTLSServerName(nil) != "" {
		t.Fatal("nil")
	}
	got := dashboardTLSServerName(&cfg.Cluster{
		OAuthURL:      "https://auth.example",
		ControllerURL: "https://controller.example",
		DashboardURL:  "https://dash.example:443",
	})
	if got != "dash.example" {
		t.Fatalf("%q", got)
	}
}

func TestDashboardHTTPClient(t *testing.T) {
	t.Parallel()
	hc, err := dashboardHTTPClient(nil)
	if err != nil || hc != http.DefaultClient {
		t.Fatalf("no pin %v %v", hc, err)
	}
	if _, err := dashboardHTTPClient(&cfg.Cluster{TLSPin: "not-base64"}); err == nil {
		t.Fatal("bad pin")
	}
}

func TestDashboardDoJSONSuccessAndErrors(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization=%q", got)
		}
		switch r.URL.Path {
		case "/ok":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "a1"})
		case "/created":
			w.WriteHeader(http.StatusNoContent)
		case "/err-json":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "login required"})
		case "/err-empty":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "nope")
		}
	}))
	defer srv.Close()

	var out map[string]string
	if err := dashboardDoJSON(srv.Client(), http.MethodGet, srv.URL+"/ok", "tok", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != "a1" {
		t.Fatalf("%v", out)
	}
	if err := dashboardDoJSON(srv.Client(), http.MethodPost, srv.URL+"/created", "tok", map[string]string{"n": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	err := dashboardDoJSON(srv.Client(), http.MethodGet, srv.URL+"/err-json", "tok", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "login required") {
		t.Fatalf("json error: %v", err)
	}
	err = dashboardDoJSON(srv.Client(), http.MethodGet, srv.URL+"/err-empty", "tok", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("empty error: %v", err)
	}
	err = dashboardDoJSON(srv.Client(), http.MethodGet, srv.URL+"/plain", "tok", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("plain error: %v", err)
	}
}
