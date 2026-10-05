package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestAuthorizeLoginHTMLMatchesDashboard(t *testing.T) {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"flynn-cli"},
		"redirect_uri":          {"http://127.0.0.1:8085/"},
		"code_challenge":        {"abc"},
		"code_challenge_method": {"S256"},
	}
	got := authorizeLoginHTML(q, "invalid credentials")
	for _, want := range []string{"login-card", "brand-mark", "btn-primary", "Log in", "type=\"email\"", "invalid credentials", "127.0.0.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q", want)
		}
	}
}
