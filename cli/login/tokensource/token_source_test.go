package tokensource

import (
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
