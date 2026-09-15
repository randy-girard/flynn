package tokensource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flynn/flynn/cli/login/internal/oauth"
	"golang.org/x/oauth2"
)

func TestCacheRejectsInvalidIssuer(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := c.GetToken("://bad", "flynn-cli", ""); err == nil {
		t.Fatal("invalid issuer URL must fail")
	}
	if err := c.SetToken("://bad", "flynn-cli", &oauth2.Token{}); err == nil {
		t.Fatal("invalid issuer URL must fail on set")
	}
}

func TestCacheMissingAndCorrupt(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := c.GetToken("https://issuer.example", "flynn-cli", ""); err != ErrTokenNotFound {
		t.Fatalf("missing: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "issuer.example")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "flynn-cli.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	c = NewTokenCache(filepath.Dir(dir))
	if _, err := c.GetToken("https://issuer.example", "flynn-cli", ""); err == nil {
		t.Fatal("corrupt JSON must fail")
	}
}

func TestCacheRoundTripRefreshAndAudience(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	issuer := "https://issuer.example"
	issued := time.Now().UTC().Truncate(time.Second)
	refreshExp := issued.Add(24 * time.Hour)
	accessExp := issued.Add(time.Hour)

	tok := (&oauth2.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		Expiry:       accessExp,
	}).WithExtra(map[string]interface{}{
		oauth.RefreshTokenExpiry:    refreshExp,
		oauth.RefreshTokenIssueTime: issued,
		"audience":                  "https://controller.example",
	})
	if err := c.SetToken(issuer, "flynn-cli", tok); err != nil {
		t.Fatal(err)
	}

	got, err := c.GetToken(issuer, "flynn-cli", "https://controller.example")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access-1" || got.RefreshToken != "refresh-1" {
		t.Fatalf("audience token: %+v", got)
	}

	refresh, err := c.GetToken(issuer, "flynn-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	if refresh.AccessToken != "refresh-1" || refresh.TokenType != "RefreshToken" {
		t.Fatalf("refresh token: %+v", refresh)
	}

	if _, err := c.GetToken(issuer, "flynn-cli", "https://other.example"); err != ErrTokenNotFound {
		t.Fatalf("unknown audience: %v", err)
	}

	dir, filename, err := c.(*cache).filepath(issuer, "flynn-cli")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, filename))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("token cache mode=%v, want 0600", st.Mode().Perm())
	}
}

func TestCacheDoesNotDowngradeNewerRefreshToken(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	issuer := "https://issuer.example"
	newer := time.Now().UTC().Truncate(time.Second)
	older := newer.Add(-time.Hour)

	first := (&oauth2.Token{RefreshToken: "new-refresh"}).WithExtra(map[string]interface{}{
		oauth.RefreshTokenIssueTime: newer,
		oauth.RefreshTokenExpiry:    newer.Add(24 * time.Hour),
		"audience":                  "https://controller.example",
	})
	if err := c.SetToken(issuer, "flynn-cli", first); err != nil {
		t.Fatal(err)
	}
	stale := (&oauth2.Token{RefreshToken: "old-refresh"}).WithExtra(map[string]interface{}{
		oauth.RefreshTokenIssueTime: older,
		oauth.RefreshTokenExpiry:    older.Add(24 * time.Hour),
		"audience":                  "https://controller.example",
	})
	if err := c.SetToken(issuer, "flynn-cli", stale); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetToken(issuer, "flynn-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "new-refresh" {
		t.Fatalf("stale refresh overwrote cache: %q", got.RefreshToken)
	}
}
