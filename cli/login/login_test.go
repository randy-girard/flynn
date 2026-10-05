package login

import (
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
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
