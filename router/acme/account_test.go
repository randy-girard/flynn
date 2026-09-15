package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/inconshreveable/log15"
)

func TestNewAccountFromConfig(t *testing.T) {
	if _, err := NewAccountFromConfig(nil); err == nil {
		t.Fatal("nil config")
	}
	if _, err := NewAccountFromConfig(&ct.ACMEConfig{Enabled: false, AccountKey: "k"}); err == nil {
		t.Fatal("disabled")
	}
	if _, err := NewAccountFromConfig(&ct.ACMEConfig{Enabled: true}); err == nil {
		t.Fatal("missing key")
	}
	a, err := NewAccountFromConfig(&ct.ACMEConfig{
		Enabled:              true,
		AccountKey:           "pem-key",
		ContactEmail:         "ops@example.com",
		TermsOfServiceAgreed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Key != "pem-key" || !a.TermsOfServiceAgreed || len(a.Contacts) != 1 || a.Contacts[0] != "ops@example.com" {
		t.Fatalf("%+v", a)
	}
}

func TestAccountPrivateKeyRoundTrip(t *testing.T) {
	if _, err := (&Account{}).PrivateKey(); err == nil {
		t.Fatal("empty key")
	}
	if _, err := (&Account{Key: "not-pem"}).PrivateKey(); err == nil {
		t.Fatal("invalid pem")
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	got, err := (&Account{Key: pemKey}).PrivateKey()
	if err != nil || got.D.Cmp(priv.D) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if (&Account{Key: "short"}).KeyID() != "unknown" {
		t.Fatal("short key id")
	}
	if id := (&Account{Key: pemKey}).KeyID(); id == "" || id == "unknown" {
		t.Fatalf("key id %q", id)
	}
}

func TestHTTP01Responder(t *testing.T) {
	r := NewResponder(log15.New())
	r.SetChallenge("tok", "key-auth")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/acme-challenge/tok", nil))
	if rec.Code != 200 || rec.Body.String() != "key-auth" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/.well-known/acme-challenge/other", nil))
	if rec.Code != 404 {
		t.Fatalf("missing token=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/not-acme", nil))
	if rec.Code != 404 {
		t.Fatalf("wrong path=%d", rec.Code)
	}

	r.RemoveChallenge("tok")
	got, ok := r.GetChallenge("tok")
	if ok || got != "" {
		t.Fatal("removed")
	}
}
