package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/context"
)

func TestValidateAuthorizeQuery(t *testing.T) {
	ok := url.Values{
		"response_type":         {"code"},
		"code_challenge":        {"abc"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {"http://127.0.0.1:8085/"},
	}
	if err := validateAuthorizeQuery(ok); err != nil {
		t.Fatal(err)
	}
	if err := validateAuthorizeQuery(url.Values{"response_type": {"token"}}); err == nil || !strings.Contains(err.Error(), "response_type") {
		t.Fatalf("%v", err)
	}
	if err := validateAuthorizeQuery(url.Values{"response_type": {"code"}}); err == nil || !strings.Contains(err.Error(), "code_challenge") {
		t.Fatalf("%v", err)
	}
	if err := validateAuthorizeQuery(url.Values{
		"response_type": {"code"}, "code_challenge": {"abc"}, "code_challenge_method": {"plain"},
	}); err == nil || !strings.Contains(err.Error(), "S256") {
		t.Fatalf("%v", err)
	}
	if err := validateAuthorizeQuery(url.Values{
		"response_type": {"code"}, "code_challenge": {"abc"}, "code_challenge_method": {"S256"},
	}); err == nil || !strings.Contains(err.Error(), "redirect_uri") {
		t.Fatalf("%v", err)
	}
}

func TestVerifyPKCE(t *testing.T) {
	if verifyPKCE("", "x") || verifyPKCE("short", "abc") {
		t.Fatal("empty/short")
	}
	verifier := strings.Repeat("a", 43)
	sum := sha256.Sum256([]byte(verifier))
	chal := base64.RawURLEncoding.EncodeToString(sum[:])
	if !verifyPKCE(verifier, chal) {
		t.Fatal("matching PKCE")
	}
	if verifyPKCE(verifier+"!", chal) {
		t.Fatal("invalid verifier charset")
	}
	if verifyPKCE(verifier, "not-the-challenge") {
		t.Fatal("mismatch")
	}
}

func TestOAuthOutOfBandRedirect(t *testing.T) {
	if !isOAuthOutOfBandRedirect("urn:ietf:wg:oauth:2.0:oob") {
		t.Fatal("oob")
	}
	if isOAuthOutOfBandRedirect("http://127.0.0.1:8085/") || isOAuthOutOfBandRedirect("") {
		t.Fatal("http callback is not OOB")
	}
}

func TestOAuthHTTPRedirectLocation(t *testing.T) {
	got, err := oauthHTTPRedirectLocation("http://127.0.0.1:8085/", "abc", "st")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("code") != "abc" || u.Query().Get("state") != "st" {
		t.Fatalf("%s", got)
	}
	if _, err := oauthHTTPRedirectLocation("urn:ietf:wg:oauth:2.0:oob", "abc", ""); err == nil {
		t.Fatal("urn must fail")
	}
	if _, err := oauthHTTPRedirectLocation("http://", "abc", ""); err == nil {
		t.Fatal("missing host")
	}
}

func TestOAuthUnsupportedGrant(t *testing.T) {
	api := &controllerAPI{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=client_credentials"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	api.OAuthToken(context.Background(), rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "unsupported_grant_type" {
		t.Fatalf("%v", body)
	}
}
