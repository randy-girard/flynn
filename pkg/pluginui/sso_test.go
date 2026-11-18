package pluginui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKID = "flynn-dashboard-ec-p256"

type jwksServer struct {
	*httptest.Server
	hits atomic.Int32
}

func newJWKS(t *testing.T, kid string, key *ecdsa.PrivateKey) *jwksServer {
	t.Helper()
	x := make([]byte, 32)
	y := make([]byte, 32)
	key.X.FillBytes(x)
	key.Y.FillBytes(y)
	s := &jwksServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": kid,
			"x": base64.RawURLEncoding.EncodeToString(x),
			"y": base64.RawURLEncoding.EncodeToString(y),
		}}})
	}))
	t.Cleanup(s.Close)
	return s
}

func genKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func validClaims() map[string]any {
	return map[string]any{
		"iss":         "flynn-dashboard",
		"aud":         "flynn-plugin-ui",
		"sub":         "user-1",
		"app_id":      "demo",
		"plugin":      "kafka",
		"exp":         time.Now().Add(10 * time.Minute).Unix(),
		"permissions": []string{"app:resources:write"},
	}
}

func sign(t *testing.T, key *ecdsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func goodHeader() map[string]any {
	return map[string]any{"alg": "ES256", "typ": "JWT", "kid": testKID}
}

func TestSSOAcceptsValidToken(t *testing.T) {
	key := genKey(t)
	srv := newJWKS(t, testKID, key)
	v := NewVerifier(srv.URL, "kafka")
	c, err := v.Verify(sign(t, key, goodHeader(), validClaims()))
	if err != nil {
		t.Fatal(err)
	}
	if c.AppID != "demo" || c.Plugin != "kafka" || c.Sub != "user-1" {
		t.Fatalf("%+v", c)
	}
}

func TestSSORejectsMissingClaimsAndForeignPlugin(t *testing.T) {
	key := genKey(t)
	other := genKey(t)
	srv := newJWKS(t, testKID, key)
	v := NewVerifier(srv.URL, "kafka")

	mut := func(f func(m map[string]any)) map[string]any {
		m := validClaims()
		f(m)
		return m
	}
	cases := map[string]struct {
		tok string
	}{
		"no aud":       {sign(t, key, goodHeader(), mut(func(m map[string]any) { delete(m, "aud") }))},
		"wrong aud":    {sign(t, key, goodHeader(), mut(func(m map[string]any) { m["aud"] = "other" }))},
		"no exp":       {sign(t, key, goodHeader(), mut(func(m map[string]any) { delete(m, "exp") }))},
		"expired":      {sign(t, key, goodHeader(), mut(func(m map[string]any) { m["exp"] = time.Now().Add(-time.Hour).Unix() }))},
		"no iss":       {sign(t, key, goodHeader(), mut(func(m map[string]any) { delete(m, "iss") }))},
		"wrong iss":    {sign(t, key, goodHeader(), mut(func(m map[string]any) { m["iss"] = "evil" }))},
		"no plugin":    {sign(t, key, goodHeader(), mut(func(m map[string]any) { delete(m, "plugin") }))},
		"foreign plug": {sign(t, key, goodHeader(), mut(func(m map[string]any) { m["plugin"] = "billing" }))},
		"no kid":       {sign(t, key, map[string]any{"alg": "ES256"}, validClaims())},
		"wrong kid":    {sign(t, key, map[string]any{"alg": "ES256", "kid": "nope"}, validClaims())},
		"alg none":     {sign(t, key, map[string]any{"alg": "none", "kid": testKID}, validClaims())},
		"alg hs256":    {sign(t, key, map[string]any{"alg": "HS256", "kid": testKID}, validClaims())},
		"wrong signer": {sign(t, other, goodHeader(), validClaims())},
		"garbage":      {"a.b.c"},
		"two segments": {"a.b"},
		"empty":        {""},
		"no sub":       {sign(t, key, goodHeader(), mut(func(m map[string]any) { delete(m, "sub") }))},
	}
	for name, tc := range cases {
		if _, err := v.Verify(tc.tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a token for another plugin verifies on that plugin only
	tok := sign(t, key, goodHeader(), mut(func(m map[string]any) { m["plugin"] = "billing" }))
	if _, err := NewVerifier(srv.URL, "billing").Verify(tok); err != nil {
		t.Fatalf("billing token on billing: %v", err)
	}
	if _, err := NewVerifier(srv.URL, "kafka").Verify(tok); err == nil {
		t.Fatal("billing token accepted by kafka")
	}
}

func TestSSOFailsClosedWithoutPluginOrJWKS(t *testing.T) {
	key := genKey(t)
	srv := newJWKS(t, testKID, key)
	tok := sign(t, key, goodHeader(), validClaims())
	if _, err := NewVerifier(srv.URL, "").Verify(tok); err == nil {
		t.Fatal("verifier without a plugin name accepted a token")
	}
	if _, err := NewVerifier("", "kafka").Verify(tok); err == nil {
		t.Fatal("verifier without JWKS URL accepted a token")
	}
	if _, err := NewVerifier("http://127.0.0.1:1/jwks", "kafka").Verify(tok); err == nil {
		t.Fatal("unreachable JWKS accepted a token")
	}
}

func TestJWKSCachedAndTimeBounded(t *testing.T) {
	key := genKey(t)
	srv := newJWKS(t, testKID, key)
	v := NewVerifier(srv.URL, "kafka")
	for i := 0; i < 5; i++ {
		if _, err := v.Verify(sign(t, key, goodHeader(), validClaims())); err != nil {
			t.Fatal(err)
		}
	}
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("JWKS fetched %d times, want 1", n)
	}
	if v.client().Timeout <= 0 || v.client().Timeout > 5*time.Second {
		t.Fatalf("jwks client timeout = %v", v.client().Timeout)
	}
}

func TestJWKSRefetchesOnUnknownKIDAndServesStaleOnError(t *testing.T) {
	key := genKey(t)
	srv := newJWKS(t, testKID, key)
	v := NewVerifier(srv.URL, "kafka")
	now := time.Now()
	v.Now = func() time.Time { return now }
	if _, err := v.Verify(sign(t, key, goodHeader(), validClaims())); err != nil {
		t.Fatal(err)
	}
	// unknown kid right after: bounded refetch, still refused
	if _, err := v.Verify(sign(t, key, map[string]any{"alg": "ES256", "kid": "rotated"}, validClaims())); err == nil {
		t.Fatal("unknown kid accepted")
	}
	// after TTL with the server down, stale keys still verify real tokens
	srv.Close()
	now = now.Add(10 * time.Minute)
	if _, err := v.Verify(sign(t, key, goodHeader(), validClaims())); err != nil {
		t.Fatalf("stale JWKS not used: %v", err)
	}
}

func TestEmptyPermissionsDenyWrite(t *testing.T) {
	if CanWrite(nil) || CanWrite([]string{}) {
		t.Fatal("empty permissions allowed write")
	}
	if CanWrite([]string{"app:resources:read"}) || CanWrite([]string{"app:overview:read", "app:logs:read"}) {
		t.Fatal("read-only permissions allowed write")
	}
	for _, p := range []string{"app:resources:write", "app:write", "app:admin", "*", "cluster:admin"} {
		if !CanWrite([]string{"app:resources:read", p}) {
			t.Errorf("%s should allow write", p)
		}
	}
	if Can(nil, "app:github:write") || !Can([]string{"app:github:write"}, "app:github:write") || Can([]string{"app:resources:write"}, "app:github:write") {
		t.Fatal("Can mismatch")
	}
	if !Can([]string{"app:write"}, "app:deploy") || !Can([]string{"app:deploy"}, "app:deploy") {
		t.Fatal("deploy grants")
	}
}

func TestRequireWriteHandler(t *testing.T) {
	ok := false
	h := RequireWrite(func(*http.Request) []string { return nil }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ok = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if ok || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "permission") {
		t.Fatalf("ok=%v code=%d body=%s", ok, rec.Code, rec.Body)
	}
	h = RequireWrite(func(*http.Request) []string { return []string{"app:resources:write"} }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ok = true }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", nil))
	if !ok {
		t.Fatal("write permission refused")
	}
}
