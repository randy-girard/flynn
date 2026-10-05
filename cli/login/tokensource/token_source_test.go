package tokensource

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/cli/login/internal/oauth"
	"golang.org/x/oauth2"
)

func TestNewRejectsNonHTTPSIssuer(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := New("prod", "http://issuer.example", "https://controller.example", c, nil); err == nil {
		t.Fatal("http issuer must be rejected")
	}
}

func TestNewRequiresCachedToken(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := New("prod", "https://issuer.example", "https://controller.example", c, nil); err == nil {
		t.Fatal("missing cache must fail")
	}
}

func TestTokenSourceMetadataURLsIncludeController(t *testing.T) {
	s := &tokenSource{
		metadataURL:   "https://auth.example/.well-known/oauth-authorization-server",
		controllerURL: "https://controller.example",
	}
	got := s.metadataURLs()
	if len(got) != 2 || got[0] != s.metadataURL || got[1] != "https://controller.example/.well-known/oauth-authorization-server" {
		t.Fatalf("%v", got)
	}
	s.controllerURL = "https://auth.example"
	s.metadataURL = "https://auth.example/.well-known/oauth-authorization-server"
	got = s.metadataURLs()
	if len(got) != 1 {
		t.Fatalf("dedupe %v", got)
	}
}

func TestTokenReturnsCachedValidAccessToken(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	cluster := "prod"
	issuer := "https://issuer.example"
	audience := "https://controller.example"
	tok := (&oauth2.Token{
		AccessToken:  "live-access",
		RefreshToken: "live-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}).WithExtra(map[string]interface{}{
		oauth.RefreshTokenIssueTime: time.Now(),
		oauth.RefreshTokenExpiry:    time.Now().Add(24 * time.Hour),
		"audience":                  audience,
	})
	if err := c.SetToken(cluster, "flynn-cli", tok); err != nil {
		t.Fatal(err)
	}
	src, err := New(cluster, issuer, audience, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "live-access" {
		t.Fatalf("got %q", got.AccessToken)
	}
}

func TestDiscoverFallsBackToControllerMetadata(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer auth.Close()
	ctrl := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(oauth.IssuerMetadata{
			AuthorizationEndpoint: "https://controller.example/oauth/authorize",
			TokenEndpoint:         "https://controller.example/oauth/token",
		})
	}))
	defer ctrl.Close()

	s := &tokenSource{
		metadataURL:   auth.URL + "/.well-known/oauth-authorization-server",
		controllerURL: ctrl.URL,
		httpClient:    ctrl.Client(),
		config:        &oauth2.Config{ClientID: "flynn-cli"},
	}
	if err := s.discover(); err != nil {
		t.Fatal(err)
	}
	if s.config.Endpoint.TokenURL != "https://controller.example/oauth/token" {
		t.Fatalf("token URL %q", s.config.Endpoint.TokenURL)
	}
}

func TestDiscoverReportsLastMetadataError(t *testing.T) {
	s := &tokenSource{
		metadataURL: "https://127.0.0.1:1/.well-known/oauth-authorization-server",
		config:      &oauth2.Config{ClientID: "flynn-cli"},
	}
	if err := s.discover(); err == nil {
		t.Fatal("unreachable metadata must fail")
	}
}

func TestTokenRefreshAndMissingAudience(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "well-known") {
			_ = json.NewEncoder(w).Encode(oauth.IssuerMetadata{
				TokenEndpoint: srv.URL + "/oauth/token",
			})
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "r1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":             "a2",
			"token_type":               "Bearer",
			"refresh_token":            "r2",
			"expires_in":               60,
			"refresh_token_expires_in": 3600,
		})
	}))
	defer srv.Close()

	cache := NewTokenCache(t.TempDir())
	expired := (&oauth2.Token{
		AccessToken:  "a1",
		RefreshToken: "r1",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(-time.Minute),
	}).WithExtra(map[string]any{"audience": srv.URL})
	s := &tokenSource{
		clusterName:   "prod",
		controllerURL: srv.URL,
		metadataURL:   srv.URL + "/.well-known/oauth-authorization-server",
		cache:         cache,
		httpClient:    srv.Client(),
		config:        &oauth2.Config{ClientID: "flynn-cli"},
		t:             expired,
	}
	got, err := s.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "a2" {
		t.Fatalf("%q", got.AccessToken)
	}

	s.t = &oauth2.Token{RefreshToken: "r1", Expiry: time.Now().Add(-time.Minute)}
	s.config.Endpoint.TokenURL = srv.URL + "/oauth/token"
	if _, err := s.Token(); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("missing audience: %v", err)
	}
}

func TestDiscoverEmptyMetadataURLs(t *testing.T) {
	s := &tokenSource{config: &oauth2.Config{ClientID: "flynn-cli"}}
	if err := s.discover(); err == nil || !strings.Contains(err.Error(), "oauth discovery failed") {
		t.Fatalf("%v", err)
	}
}
