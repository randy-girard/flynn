package authorizer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"testing"
	"time"
)

func TestHasClusterAdmin(t *testing.T) {
	if !(*Token)(nil).HasClusterAdmin() {
		t.Fatal("nil token is treated as cluster admin (legacy callers)")
	}
	cluster := Token{ClusterKey: true}
	if !cluster.HasClusterAdmin() {
		t.Fatal("cluster key")
	}
	admin := Token{Scopes: []string{"cluster:admin"}}
	if !admin.HasClusterAdmin() {
		t.Fatal("cluster:admin scope")
	}
	star := Token{Scopes: []string{"*"}}
	if !star.HasClusterAdmin() {
		t.Fatal("* scope")
	}
	legacy := Token{}
	if !legacy.HasClusterAdmin() {
		t.Fatal("unsigned dashboard token with no scopes/grants")
	}
	scoped := Token{AppGrants: []AppGrant{{AppID: "app-1", Permissions: []string{"app:read"}}}}
	if scoped.HasClusterAdmin() {
		t.Fatal("app-scoped token must not be cluster admin")
	}
	if !scoped.BearerScopedToApps() {
		t.Fatal("app-scoped bearer")
	}
	if cluster.BearerScopedToApps() {
		t.Fatal("cluster key is not app-scoped")
	}
}

func TestParseTokenKeyAndValidity(t *testing.T) {
	k, err := ParseTokenKey("")
	if err != nil || k != nil {
		t.Fatalf("empty key: %v %v", k, err)
	}
	if _, err := ParseTokenKey("%%%"); err == nil {
		t.Fatal("invalid base64")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPub, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTokenKey(base64.URLEncoding.EncodeToString(rsaPub)); err == nil {
		t.Fatal("RSA key must be rejected")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	k, err = ParseTokenKey(base64.URLEncoding.EncodeToString(pub))
	if err != nil || k == nil {
		t.Fatalf("ecdsa: %v %v", k, err)
	}

	d, err := ParseTokenMaxValidity("")
	if err != nil || d != time.Hour {
		t.Fatalf("default validity: %v %v", d, err)
	}
	d, err = ParseTokenMaxValidity("30")
	if err != nil || d != 30*time.Second {
		t.Fatalf("seconds: %v %v", d, err)
	}
	if _, err := ParseTokenMaxValidity("nope"); err == nil {
		t.Fatal("invalid duration")
	}
}

func TestAuthorizeKeyConstantTimeMatch(t *testing.T) {
	a := New([]string{"alpha-key", "beta-key"}, []string{"id-a", "id-b"}, nil, time.Hour)
	if _, err := a.AuthorizeKey(""); err != ErrInvalid {
		t.Fatalf("empty: %v", err)
	}
	if _, err := a.AuthorizeKey("wrong-key"); err != ErrInvalid {
		t.Fatalf("wrong: %v", err)
	}
	// Different length must not compare as equal even if it is a prefix.
	if _, err := a.AuthorizeKey("alpha-ke"); err != ErrInvalid {
		t.Fatalf("prefix: %v", err)
	}
	tok, err := a.AuthorizeKey("beta-key")
	if err != nil || !tok.ClusterKey || tok.ID != "id-b" {
		t.Fatalf("match: %+v %v", tok, err)
	}

	noIDs := New([]string{"only-key"}, nil, nil, time.Hour)
	tok, err = noIDs.AuthorizeKey("only-key")
	if err != nil || !tok.ClusterKey || tok.ID != "" {
		t.Fatalf("no ids: %+v %v", tok, err)
	}
}

func TestAuthorizeRequestClusterKeyAndMissing(t *testing.T) {
	a := New([]string{"cluster-secret"}, []string{"cid"}, nil, time.Hour)

	req, _ := http.NewRequest("GET", "/", nil)
	req.SetBasicAuth("", "cluster-secret")
	tok, err := a.AuthorizeRequest(req)
	if err != nil || !tok.ClusterKey || tok.ID != "cid" {
		t.Fatalf("basic key: %+v %v", tok, err)
	}

	req, _ = http.NewRequest("GET", "/", nil)
	if _, err := a.AuthorizeRequest(req); err != ErrInvalid {
		t.Fatalf("missing auth: %v", err)
	}

	req, _ = http.NewRequest("GET", "/", nil)
	req.SetBasicAuth("", "wrong")
	if _, err := a.AuthorizeRequest(req); err != ErrInvalid {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestVerifyASN1RejectsGarbage(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if verifyASN1(&priv.PublicKey, make([]byte, 32), []byte("not-asn1")) {
		t.Fatal("garbage signature")
	}
}

func TestAuthorizeTokenRejectsMissingKeyAndEncoding(t *testing.T) {
	a := New(nil, nil, nil, time.Hour)
	if _, err := a.AuthorizeToken("anything"); err != ErrInvalid {
		t.Fatalf("no token key: %v", err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a = New(nil, nil, &priv.PublicKey, time.Hour)
	if _, err := a.AuthorizeToken("not-base64%%%"); err == nil {
		t.Fatal("invalid encoding")
	}
	if _, err := a.AuthorizeToken("Bearer aW52YWxpZA=="); err == nil {
		t.Fatal("garbage protobuf must fail")
	}
}
