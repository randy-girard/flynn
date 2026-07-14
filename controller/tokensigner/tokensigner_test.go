package tokensigner

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"

	api "github.com/flynn/flynn/controller/api"
	"github.com/flynn/flynn/controller/authorizer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// newKeyPair returns a signer and the base64url PKIX public key that
// authorizer.ParseTokenKey expects, so tests can round-trip sign -> verify.
func newKeyPair(t *testing.T) (*Signer, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %s", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %s", err)
	}
	return New(priv), base64.URLEncoding.EncodeToString(pub)
}

// TestSignRoundTrip verifies that a token minted by Sign is accepted by the
// authorizer and that its scopes and app grants survive verification.
func TestSignRoundTrip(t *testing.T) {
	signer, pubKey := newKeyPair(t)

	pk, err := authorizer.ParseTokenKey(pubKey)
	if err != nil {
		t.Fatalf("parse token key: %s", err)
	}
	auth := authorizer.New(nil, nil, pk, time.Hour)

	now := time.Now()
	tokenStr, err := signer.Sign(&api.AccessToken{
		UserEmail:  "build:demo",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{"build:artifacts"},
		AppGrants: []*api.AppGrant{
			{AppId: "app-1", Permissions: []string{"app:write"}},
		},
	})
	if err != nil {
		t.Fatalf("sign: %s", err)
	}

	tok, err := auth.AuthorizeToken(tokenStr)
	if err != nil {
		t.Fatalf("authorize minted token: %s", err)
	}
	if tok.ClusterKey {
		t.Fatalf("minted token must not be a cluster key")
	}
	if tok.User != "build:demo" {
		t.Fatalf("user = %q, want build:demo", tok.User)
	}
	if len(tok.Scopes) != 1 || tok.Scopes[0] != "build:artifacts" {
		t.Fatalf("scopes = %v, want [build:artifacts]", tok.Scopes)
	}
	if len(tok.AppGrants) != 1 ||
		tok.AppGrants[0].AppID != "app-1" ||
		len(tok.AppGrants[0].Permissions) != 1 ||
		tok.AppGrants[0].Permissions[0] != "app:write" {
		t.Fatalf("app grants = %v, want app-1:[app:write]", tok.AppGrants)
	}
}

// TestExpiredTokenRejected verifies the authorizer rejects a minted token whose
// expiry is in the past.
func TestExpiredTokenRejected(t *testing.T) {
	signer, pubKey := newKeyPair(t)
	pk, _ := authorizer.ParseTokenKey(pubKey)
	auth := authorizer.New(nil, nil, pk, time.Hour)

	now := time.Now()
	tokenStr, err := signer.Sign(&api.AccessToken{
		IssueTime:  timestamppb.New(now.Add(-30 * time.Minute)),
		ExpireTime: timestamppb.New(now.Add(-15 * time.Minute)),
		Scopes:     []string{"build:artifacts"},
	})
	if err != nil {
		t.Fatalf("sign: %s", err)
	}
	if _, err := auth.AuthorizeToken(tokenStr); err == nil {
		t.Fatalf("expected expired token to be rejected")
	}
}

// TestValidityCeilingRejected verifies a token exceeding the authorizer's max
// validity is rejected even if not yet expired.
func TestValidityCeilingRejected(t *testing.T) {
	signer, pubKey := newKeyPair(t)
	pk, _ := authorizer.ParseTokenKey(pubKey)
	auth := authorizer.New(nil, nil, pk, 15*time.Minute)

	now := time.Now()
	tokenStr, err := signer.Sign(&api.AccessToken{
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(time.Hour)),
		Scopes:     []string{"build:artifacts"},
	})
	if err != nil {
		t.Fatalf("sign: %s", err)
	}
	if _, err := auth.AuthorizeToken(tokenStr); err == nil {
		t.Fatalf("expected token exceeding max validity to be rejected")
	}
}

// TestParseSigningKey verifies ParseSigningKey round-trips a PKCS#8 key and
// returns nil for an empty string.
func TestParseSigningKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %s", err)
	}
	b, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %s", err)
	}
	s, err := ParseSigningKey(base64.URLEncoding.EncodeToString(b))
	if err != nil {
		t.Fatalf("parse signing key: %s", err)
	}
	if s == nil {
		t.Fatalf("expected non-nil signer")
	}

	nilSigner, err := ParseSigningKey("")
	if err != nil {
		t.Fatalf("empty key should not error: %s", err)
	}
	if nilSigner != nil {
		t.Fatalf("empty key should return nil signer")
	}
}
