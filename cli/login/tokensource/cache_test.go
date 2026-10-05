package tokensource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/randy-girard/flynn/cli/login/internal/oauth"
	"golang.org/x/oauth2"
)

func TestCacheRejectsInvalidClusterName(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := c.GetToken("../escape", "flynn-cli", ""); err == nil {
		t.Fatal("path cluster name must fail")
	}
	if err := c.SetToken("a/b", "flynn-cli", &oauth2.Token{}); err == nil {
		t.Fatal("slash cluster name must fail on set")
	}
	if _, err := c.GetToken("", "flynn-cli", ""); err != ErrTokenNotFound {
		t.Fatalf("empty cluster name: %v", err)
	}
}

func TestCacheMissingAndCorrupt(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	if _, err := c.GetToken("prod", "flynn-cli", ""); err != ErrTokenNotFound {
		t.Fatalf("missing: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "prod")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "flynn-cli.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	c = NewTokenCache(filepath.Dir(dir))
	if _, err := c.GetToken("prod", "flynn-cli", ""); err == nil {
		t.Fatal("corrupt JSON must fail")
	}
}

func TestCacheRoundTripRefreshAndAudience(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	cluster := "prod"
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
	if err := c.SetToken(cluster, "flynn-cli", tok); err != nil {
		t.Fatal(err)
	}

	got, err := c.GetToken(cluster, "flynn-cli", "https://controller.example")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "access-1" || got.RefreshToken != "refresh-1" {
		t.Fatalf("audience token: %+v", got)
	}

	refresh, err := c.GetToken(cluster, "flynn-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	if refresh.AccessToken != "refresh-1" || refresh.TokenType != "RefreshToken" {
		t.Fatalf("refresh token: %+v", refresh)
	}

	if _, err := c.GetToken(cluster, "flynn-cli", "https://other.example"); err != ErrTokenNotFound {
		t.Fatalf("other audience: %v", err)
	}
}

func TestCacheIsolatesClusterNames(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	issued := time.Now().UTC().Truncate(time.Second)
	makeTok := func(access, refresh, audience string) *oauth2.Token {
		return (&oauth2.Token{
			AccessToken:  access,
			RefreshToken: refresh,
			TokenType:    "Bearer",
			Expiry:       issued.Add(time.Hour),
		}).WithExtra(map[string]interface{}{
			oauth.RefreshTokenExpiry:    issued.Add(24 * time.Hour),
			oauth.RefreshTokenIssueTime: issued,
			"audience":                  audience,
		})
	}
	if err := c.SetToken("alpha", "flynn-cli", makeTok("a-access", "a-refresh", "https://controller.a")); err != nil {
		t.Fatal(err)
	}
	if err := c.SetToken("beta", "flynn-cli", makeTok("b-access", "b-refresh", "https://controller.b")); err != nil {
		t.Fatal(err)
	}
	a, err := c.GetToken("alpha", "flynn-cli", "https://controller.a")
	if err != nil || a.AccessToken != "a-access" {
		t.Fatalf("alpha: %+v %v", a, err)
	}
	b, err := c.GetToken("beta", "flynn-cli", "https://controller.b")
	if err != nil || b.AccessToken != "b-access" {
		t.Fatalf("beta: %+v %v", b, err)
	}
	if _, err := c.GetToken("alpha", "flynn-cli", "https://controller.b"); err != ErrTokenNotFound {
		t.Fatalf("alpha should not see beta audience: %v", err)
	}
}

func TestCacheKeepsNewerRefreshToken(t *testing.T) {
	c := NewTokenCache(t.TempDir())
	issued := time.Now().UTC().Truncate(time.Second)
	first := (&oauth2.Token{
		AccessToken:  "access-new",
		RefreshToken: "refresh-new",
		TokenType:    "Bearer",
		Expiry:       issued.Add(time.Hour),
	}).WithExtra(map[string]interface{}{
		oauth.RefreshTokenExpiry:    issued.Add(24 * time.Hour),
		oauth.RefreshTokenIssueTime: issued,
		"audience":                  "https://controller.example",
	})
	if err := c.SetToken("prod", "flynn-cli", first); err != nil {
		t.Fatal(err)
	}
	stale := (&oauth2.Token{
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		TokenType:    "Bearer",
		Expiry:       issued.Add(2 * time.Hour),
	}).WithExtra(map[string]interface{}{
		oauth.RefreshTokenExpiry:    issued.Add(24 * time.Hour),
		oauth.RefreshTokenIssueTime: issued.Add(-time.Minute),
		"audience":                  "https://controller.example",
	})
	if err := c.SetToken("prod", "flynn-cli", stale); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetToken("prod", "flynn-cli", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "refresh-new" {
		t.Fatalf("stale refresh overwrote newer token: %q", got.RefreshToken)
	}
}
