package tokensource

import (
	"testing"
	"time"

	"github.com/flynn/flynn/cli/login/internal/oauth"
	"golang.org/x/oauth2"
)

func TestNewRejectsNonHTTPSIssuer(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := New("http://issuer.example", "https://controller.example", c); err == nil {
		t.Fatal("http issuer must be rejected")
	}
}

func TestNewRequiresCachedToken(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := New("https://issuer.example", "https://controller.example", c); err == nil {
		t.Fatal("missing cache must fail")
	}
}

func TestTokenReturnsCachedValidAccessToken(t *testing.T) {
	c := NewTokenCache(t.TempDir())
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
	if err := c.SetToken(issuer, "flynn-cli", tok); err != nil {
		t.Fatal(err)
	}
	src, err := New(issuer, audience, c)
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
