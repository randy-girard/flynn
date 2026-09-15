package login

import (
	"strings"
	"testing"

	"github.com/flynn/flynn/cli/config"
	"github.com/flynn/go-docopt"
	"golang.org/x/oauth2"
)

func TestLooksLikeIssuerURL(t *testing.T) {
	if looksLikeIssuerURL("") || looksLikeIssuerURL("default") {
		t.Fatal("cluster names are not issuer URLs")
	}
	if !looksLikeIssuerURL("https://id.example") || !looksLikeIssuerURL("http://id.example") {
		t.Fatal("scheme URLs")
	}
	if !looksLikeIssuerURL("http:legacy") {
		t.Fatal("http: prefix without slashes")
	}
}

func TestIssuerFromCluster(t *testing.T) {
	c := &config.Cluster{DashboardURL: " https://dash.example "}
	if issuerFromCluster(c) != "https://dash.example" {
		t.Fatalf("%q", issuerFromCluster(c))
	}
	c.OAuthURL = "https://id.example"
	if issuerFromCluster(c) != "https://id.example" {
		t.Fatal("OAuthURL takes precedence")
	}
}

func TestUseOOBFlag(t *testing.T) {
	args := &docopt.Args{Bool: map[string]bool{"--oob-code": true}}
	if !useOOB(args) {
		t.Fatal("--oob-code")
	}
}

func TestBuildAuthCodeURLPKCE(t *testing.T) {
	cfg := &oauth2.Config{
		ClientID:    "flynn-cli",
		RedirectURL: "http://127.0.0.1:8085/callback",
		Endpoint:    oauth2.Endpoint{AuthURL: "https://id.example/auth"},
	}
	info := buildAuthCodeURL(cfg)
	if info.Verifier == "" || info.Nonce == "" || info.State == "" {
		t.Fatalf("%+v", info)
	}
	if !strings.Contains(info.URL, "code_challenge_method=S256") {
		t.Fatalf("pkce missing: %s", info.URL)
	}
	if !strings.Contains(info.URL, "code_challenge=") {
		t.Fatal("code_challenge")
	}

	cfg.RedirectURL = oobRedirectURI
	oob := buildAuthCodeURL(cfg)
	if oob.State != "" {
		t.Fatal("OOB flow must not set CSRF state")
	}
}
