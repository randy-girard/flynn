package login

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/cli/config"
	"github.com/randy-girard/flynn/cli/login/internal/oauth"
	"golang.org/x/oauth2"
)

func TestCredentialsFillMissingRequiresFlagsWhenNonInteractive(t *testing.T) {
	c := Credentials{}
	if err := c.FillMissing(false); err == nil {
		t.Fatal("expected error")
	}
	c.Email = "ada@example.com"
	if err := c.FillMissing(false); err == nil {
		t.Fatal("password required")
	}
	c.Password = "secret"
	if err := c.FillMissing(false); err != nil {
		t.Fatal(err)
	}
	c.Email = "ada"
	if err := c.FillMissing(false); err == nil {
		t.Fatal("handle is not an email")
	}
}

func TestUseOAuthFlag(t *testing.T) {
	if useOOB(&docopt.Args{Bool: map[string]bool{}}) {
		t.Fatal("password grant is the default")
	}
	if !useOOB(&docopt.Args{Bool: map[string]bool{"--oauth": true}}) {
		t.Fatal("--oauth")
	}
	if !useOOB(&docopt.Args{Bool: map[string]bool{"--oob-code": true}}) {
		t.Fatal("--oob-code alias")
	}
}

func TestOAuthIssuerCandidatesPrefersAuth(t *testing.T) {
	got := oauthIssuerCandidates("https://controller.demo.local", "https://controller.demo.local")
	if len(got) != 2 || got[0] != "https://auth.demo.local" || got[1] != "https://controller.demo.local" {
		t.Fatalf("%v", got)
	}
	deduped := oauthIssuerCandidates("https://auth.demo.local", "https://controller.demo.local")
	if len(deduped) != 2 || deduped[0] != "https://auth.demo.local" || deduped[1] != "https://controller.demo.local" {
		t.Fatalf("%v", deduped)
	}
}

func TestFlynnAuthIssuer(t *testing.T) {
	if got := flynnAuthIssuer("https://controller.1.localflynn.com", "https://controller.1.localflynn.com"); got != "https://auth.1.localflynn.com" {
		t.Fatalf("got %q", got)
	}
	if got := flynnAuthIssuer("https://auth.1.localflynn.com", "https://controller.1.localflynn.com"); got != "https://auth.1.localflynn.com" {
		t.Fatalf("got %q", got)
	}
	if got := flynnAuthIssuer("https://login.example.com", "https://login.example.com"); got != "" {
		t.Fatalf("external issuer %q", got)
	}
	if got := flynnControllerIssuer("https://controller.1.localflynn.com", "https://auth.1.localflynn.com"); got != "https://controller.1.localflynn.com" {
		t.Fatalf("controller %q", got)
	}
}

func TestFlynnControllerIssuer(t *testing.T) {
	if got := flynnControllerIssuer("https://controller.1.localflynn.com", "https://auth.1.localflynn.com"); got != "https://controller.1.localflynn.com" {
		t.Fatalf("controller %q", got)
	}
}

func TestAuthHostsHintWhenAuthDoesNotResolve(t *testing.T) {
	got := AuthHostsHint("https://auth.this-name-should-not-resolve.invalid", "https://controller.this-name-should-not-resolve.invalid")
	if !strings.Contains(got, "auth.this-name-should-not-resolve.invalid") || !strings.Contains(got, "/etc/hosts") {
		t.Fatalf("%q", got)
	}
}

func TestRewriteOAuthEndpointsMovesControllerHost(t *testing.T) {
	meta := syntheticFlynnMetadata("https://auth.1.localflynn.com")
	meta.AuthorizationEndpoint = "https://controller.1.localflynn.com/oauth/authorize"
	meta.TokenEndpoint = "https://controller.1.localflynn.com/oauth/token"
	rewriteOAuthEndpoints(meta, "https://auth.1.localflynn.com")
	if meta.AuthorizationEndpoint != "https://auth.1.localflynn.com/oauth/authorize" {
		t.Fatalf("authorize %q", meta.AuthorizationEndpoint)
	}
	if meta.TokenEndpoint != "https://auth.1.localflynn.com/oauth/token" {
		t.Fatalf("token %q", meta.TokenEndpoint)
	}
}

func TestLooksLikeIssuerURL(t *testing.T) {
	if looksLikeIssuerURL("") || looksLikeIssuerURL("prod") || looksLikeIssuerURL("  ") {
		t.Fatal("cluster names are not issuer URLs")
	}
	if !looksLikeIssuerURL("https://controller.example") || !looksLikeIssuerURL("http://127.0.0.1:8080") {
		t.Fatal("absolute URLs")
	}
}

func TestIssuerFromCluster(t *testing.T) {
	if issuerFromCluster(nil) != "" {
		t.Fatal("nil")
	}
	got := issuerFromCluster(&config.Cluster{ControllerURL: "https://controller.demo.local", OAuthURL: "https://login.example"})
	if got != "https://auth.demo.local" {
		t.Fatalf("prefer derived auth issuer, got %q", got)
	}
	got = issuerFromCluster(&config.Cluster{OAuthURL: "https://login.example"})
	if got != "https://login.example" {
		t.Fatalf("oauth url %q", got)
	}
	got = issuerFromCluster(&config.Cluster{ControllerURL: "https://api.example"})
	if got != "https://api.example" {
		t.Fatalf("controller fallback %q", got)
	}
}

func TestLoginClusterRequiresCluster(t *testing.T) {
	if err := LoginCluster(nil, Credentials{}, false); err == nil || !strings.Contains(err.Error(), "cluster is required") {
		t.Fatalf("%v", err)
	}
	if err := LoginCluster(&config.Cluster{Name: "local"}, Credentials{}, false); err == nil || !strings.Contains(err.Error(), "no controller URL") {
		t.Fatalf("%v", err)
	}
}

func TestRewriteOAuthURL(t *testing.T) {
	if got := rewriteOAuthURL("", "https://auth.example"); got != "https://auth.example" {
		t.Fatalf("empty %q", got)
	}
	if got := rewriteOAuthURL("/oauth/token", "https://auth.example/"); got != "https://auth.example/oauth/token" {
		t.Fatalf("relative %q", got)
	}
	if got := rewriteOAuthURL("https://dashboard.1.localflynn.com/oauth/token", "https://auth.1.localflynn.com"); got != "https://auth.1.localflynn.com/oauth/token" {
		t.Fatalf("dashboard host %q", got)
	}
	keep := "https://login.example/oauth/token"
	if got := rewriteOAuthURL(keep, "https://auth.example"); got != keep {
		t.Fatalf("external issuer %q", got)
	}
	rewriteOAuthEndpoints(nil, "https://auth.example")
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty(" ", "", "ada@example.com") != "ada@example.com" {
		t.Fatal("trim skip")
	}
	if firstNonEmpty() != "" || firstNonEmpty("", "  ") != "" {
		t.Fatal("empty")
	}
}

func TestAuthHostsHintSkipsNonAuthHosts(t *testing.T) {
	if AuthHostsHint("https://controller.example", "https://controller.example") != "" {
		t.Fatal("controller host")
	}
	if AuthHostsHint("://", "https://controller.example") != "" {
		t.Fatal("invalid URL")
	}
}

func TestBuildAuthCodeURLIncludesPKCE(t *testing.T) {
	cfg := &oauth2.Config{
		ClientID:    cliClientID,
		RedirectURL: oobRedirectURI,
		Endpoint:    oauth2.Endpoint{AuthURL: "https://auth.example/oauth/authorize"},
	}
	oob := buildAuthCodeURL(cfg)
	if oob.State != "" {
		t.Fatal("OOB must not set state")
	}
	u, err := url.Parse(oob.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" {
		t.Fatalf("%v", q)
	}
	cfg.RedirectURL = "http://127.0.0.1:8085/"
	auto := buildAuthCodeURL(cfg)
	if auto.State == "" || auto.Verifier == "" {
		t.Fatal("browser login needs state and verifier")
	}
}

func TestOAuthIssuerReachable(t *testing.T) {
	if oauthIssuerReachable(nil, "http://issuer.example") {
		t.Fatal("http issuer")
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(oauth.IssuerMetadata{TokenEndpoint: "https://controller.example/oauth/token"})
	}))
	defer srv.Close()
	if !oauthIssuerReachable(srv.Client(), srv.URL) {
		t.Fatal("tls metadata")
	}
}

func TestRunRequiresConfiguredCluster(t *testing.T) {
	t.Setenv("FLYNNRC", filepath.Join(t.TempDir(), "flynnrc"))
	empty := &docopt.Args{String: map[string]string{}, Bool: map[string]bool{}}
	err := Run(empty, "")
	if err == nil || !strings.Contains(err.Error(), "no cluster") {
		t.Fatalf("%v", err)
	}
	err = Run(&docopt.Args{String: map[string]string{"<issuer-or-cluster>": "missing"}, Bool: map[string]bool{}}, "")
	if err == nil || !strings.Contains(err.Error(), "unknown cluster") {
		t.Fatalf("%v", err)
	}
	err = Run(&docopt.Args{String: map[string]string{"<issuer-or-cluster>": "a", "--cluster-name": "b"}, Bool: map[string]bool{}}, "")
	if err == nil || !strings.Contains(err.Error(), "conflicting cluster names") {
		t.Fatalf("%v", err)
	}
	err = Run(&docopt.Args{String: map[string]string{"--cluster-name": "a"}, Bool: map[string]bool{}}, "b")
	if err == nil || !strings.Contains(err.Error(), "conflicting cluster selection") {
		t.Fatalf("%v", err)
	}
}

func TestAuthenticatePasswordGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FLYNNRC", filepath.Join(home, "flynnrc"))
	if err := os.MkdirAll(filepath.Join(home, ".flynn"), 0700); err != nil {
		t.Fatal(err)
	}

	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "oauth-authorization-server"):
			_ = json.NewEncoder(w).Encode(oauth.IssuerMetadata{
				AuthorizationEndpoint: srv.URL + "/oauth/authorize",
				TokenEndpoint:         srv.URL + "/oauth/token",
			})
		case r.URL.Path == "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("grant_type") != "password" || r.Form.Get("username") != "ada@example.com" || r.Form.Get("password") != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":             "a1",
				"token_type":               "Bearer",
				"refresh_token":            "r1",
				"expires_in":               3600,
				"refresh_token_expires_in": 86400,
				"audience":                 r.Form.Get("audience"),
			})
		case r.URL.Path == "/.well-known/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"status": "healthy"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	used, err := Authenticate("prod", srv.URL, srv.URL, Credentials{Email: "ada@example.com", Password: "secret"}, false, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if used != srv.URL {
		t.Fatalf("issuer %q", used)
	}
}
