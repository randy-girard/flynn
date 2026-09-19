package githubapp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestParseRSAPrivateKeyPKCS1AndPKCS8(t *testing.T) {
	key := testRSAKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	got, err := ParseRSAPrivateKey(pkcs1)
	if err != nil || got.N.Cmp(key.N) != 0 {
		t.Fatalf("pkcs1: %+v %v", got, err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err = ParseRSAPrivateKey(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	if err != nil || got.N.Cmp(key.N) != 0 {
		t.Fatalf("pkcs8: %+v %v", got, err)
	}
	if _, err := ParseRSAPrivateKey([]byte("not-pem")); err == nil {
		t.Fatal("expected error")
	}
}

func TestSignAppJWT(t *testing.T) {
	key := testRSAKey(t)
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tok, err := SignAppJWT(42, key, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := DecodeJWTPayload(tok)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["iss"] != "42" {
		t.Fatalf("iss=%v", payload["iss"])
	}
	if _, err := SignAppJWT(0, key, now); err == nil {
		t.Fatal("app id required")
	}
	if _, err := SignAppJWT(1, nil, now); err == nil {
		t.Fatal("key required")
	}
	if !strings.HasPrefix(tok, "eyJ") {
		t.Fatalf("token %s", tok)
	}
}
