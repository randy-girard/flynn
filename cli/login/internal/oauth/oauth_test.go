package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestBuildMetadataURLRequiresHTTPS(t *testing.T) {
	if _, _, err := BuildMetadataURL("http://issuer.example"); err == nil {
		t.Fatal("http issuer must be rejected")
	}
	if _, _, err := BuildMetadataURL("://bad"); err == nil {
		t.Fatal("invalid URL must be rejected")
	}

	u, clientID, err := BuildMetadataURL("https://issuer.example")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://issuer.example/.well-known/oauth-authorization-server" {
		t.Fatalf("url=%s", u)
	}
	if clientID != "" {
		t.Fatalf("clientID=%s", clientID)
	}

	u, clientID, err = BuildMetadataURL("https://issuer.example/flynn?client_id=custom")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "https://issuer.example/.well-known/oauth-authorization-server") {
		t.Fatalf("path join url=%s", u)
	}
	if clientID != "custom" {
		t.Fatalf("client_id=%s", clientID)
	}
	if strings.Contains(u, "client_id") {
		t.Fatal("metadata URL must drop query credentials")
	}
}

func TestGetMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_ = json.NewEncoder(w).Encode(IssuerMetadata{
				AuthorizationEndpoint: "https://issuer.example/auth",
				TokenEndpoint:         "https://issuer.example/token",
			})
		case "/bad-json":
			io.WriteString(w, "{")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	meta, err := GetMetadata(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	if meta.TokenEndpoint != "https://issuer.example/token" {
		t.Fatalf("%+v", meta)
	}
	if _, err := GetMetadata(srv.URL + "/missing"); err == nil {
		t.Fatal("404 must fail")
	}
	if _, err := GetMetadata(srv.URL + "/bad-json"); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestRefreshTokenSuccessAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/token":
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "r1" {
				t.Errorf("form=%v", r.Form)
			}
			if r.Form.Get("audience") != "https://controller.example" {
				t.Errorf("audience=%s", r.Form.Get("audience"))
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"access_token":             "a2",
				"token_type":               "Bearer",
				"refresh_token":            "r2",
				"expires_in":               60,
				"refresh_token_expires_in": 3600,
				"refresh_token_issue_time": time.Now().UTC().Format(time.RFC3339),
			})
		case "/oauth-error":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(Error{Code: "invalid_grant", Description: "revoked"})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, "nope")
		}
	}))
	defer srv.Close()

	cfg := &oauth2.Config{ClientID: "flynn-cli", Endpoint: oauth2.Endpoint{TokenURL: srv.URL + "/token"}}
	tok, err := RefreshToken(cfg, &oauth2.Token{RefreshToken: "r1"}, "https://controller.example")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "a2" || tok.RefreshToken != "r2" {
		t.Fatalf("%+v", tok)
	}

	cfg.Endpoint.TokenURL = srv.URL + "/oauth-error"
	_, err = RefreshToken(cfg, &oauth2.Token{RefreshToken: "r1"}, "")
	oauthErr, ok := err.(*Error)
	if !ok || oauthErr.Code != "invalid_grant" {
		t.Fatalf("oauth error: %v", err)
	}

	cfg.Endpoint.TokenURL = srv.URL + "/fail"
	if _, err := RefreshToken(cfg, &oauth2.Token{RefreshToken: "r1"}, ""); err == nil {
		t.Fatal("non-JSON error body must fail")
	}
}

func TestOAuthErrorString(t *testing.T) {
	if (Error{Code: "invalid_grant"}).Error() != "oauth error: invalid_grant" {
		t.Fatal("code-only")
	}
	got := (Error{Code: "invalid_grant", Description: "revoked"}).Error()
	if got != "invalid_grant: revoked" {
		t.Fatalf("got %q", got)
	}
}
