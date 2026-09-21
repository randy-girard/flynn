package blobstoreauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestCacheAppID(t *testing.T) {
	id, ok := CacheAppID("/app-1-cache.tgz")
	if !ok || id != "app-1" {
		t.Fatalf("cache: %q %v", id, ok)
	}
	id, ok = CacheAppID("/app-1-docker-cache.tgz")
	if !ok || id != "app-1" {
		t.Fatalf("docker-cache must not parse as app-1-docker: %q %v", id, ok)
	}
	if _, ok := CacheAppID("/slugs/layers/x.squashfs"); ok {
		t.Fatal("slug path is not a cache path")
	}
	if _, ok := CacheAppID("/foo/bar-cache.tgz"); ok {
		t.Fatal("nested path is not a cache path")
	}
}

func TestValidCacheToken(t *testing.T) {
	key := "cluster-key"
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("app-1"))
	tok := hex.EncodeToString(mac.Sum(nil))
	if !ValidCacheToken("/app-1-cache.tgz", tok, []string{key}) {
		t.Fatal("valid token rejected")
	}
	if !ValidCacheToken("/app-1-docker-cache.tgz", tok, []string{key}) {
		t.Fatal("same HMAC covers docker-cache for the same app")
	}
	if ValidCacheToken("/app-2-cache.tgz", tok, []string{key}) {
		t.Fatal("token must not authorize another app")
	}
	if ValidCacheToken("/app-1-cache.tgz", tok[:len(tok)-2]+"00", []string{key}) {
		t.Fatal("tampered token must fail")
	}
	if ValidCacheToken("/app-1-cache.tgz", tok, []string{"other-key"}) {
		t.Fatal("wrong key must fail")
	}
}

func TestIsPublicAndBuildPaths(t *testing.T) {
	if !IsPublicPath("/.well-known/status", http.MethodGet) {
		t.Fatal("status must be public")
	}
	if !IsPublicPath("/", http.MethodHead) {
		t.Fatal("HEAD / is the unauthenticated health probe")
	}
	if IsPublicPath("/", http.MethodGet) {
		t.Fatal("GET / lists files and must require auth")
	}
	if !IsBuildUploadPath("/slugs/layers/abc.squashfs") || !IsBuildUploadPath("/tarreceive/images/x.json") {
		t.Fatal("builder upload prefixes")
	}
	if IsBuildUploadPath("/repos/x.tar") || IsBuildUploadPath("/plugins/x") {
		t.Fatal("repos/plugins are cluster-key only")
	}
}

func TestApplyKeyAndEnv(t *testing.T) {
	t.Setenv("AUTH_KEY", "from-auth")
	t.Setenv("CONTROLLER_KEY", "from-controller")
	keys := EnvKeys()
	if len(keys) != 2 || keys[0] != "from-auth" || keys[1] != "from-controller" {
		t.Fatalf("EnvKeys=%v", keys)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://blobstore.discoverd/x", nil)
	Apply(req)
	_, pass, ok := req.BasicAuth()
	if !ok || pass != "from-auth" {
		t.Fatalf("Apply basic=%v pass=%q", ok, pass)
	}

	t.Setenv("AUTH_KEY", "a, a, b")
	got := EnvKeys()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "from-controller" {
		t.Fatalf("comma AUTH_KEY: %v", got)
	}
}

func TestIsBlobstoreURL(t *testing.T) {
	u, _ := url.Parse("http://blobstore.discoverd/layers/x.squashfs")
	if !IsBlobstoreURL(u) {
		t.Fatal("blobstore.discoverd")
	}
	u, _ = url.Parse("http://blobstore.discoverd:80/x")
	if !IsBlobstoreURL(u) {
		t.Fatal("with port")
	}
	u, _ = url.Parse("http://example.com/x")
	if IsBlobstoreURL(u) {
		t.Fatal("foreign host")
	}
}

func TestTransportAddsAuthForBlobstore(t *testing.T) {
	t.Setenv("AUTH_KEY", "secret")
	t.Setenv("DISCOVERD", "")
	var gotAuth, gotHost string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAuth = req.Header.Get("Authorization")
		gotHost = req.URL.Host
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
	})
	rt := &Transport{Base: base}

	req, err := http.NewRequest(http.MethodGet, "http://example.com/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("foreign host sent Authorization=%q", gotAuth)
	}

	req, err = http.NewRequest(http.MethodGet, "http://blobstore.discoverd/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if gotHost != "blobstore.discoverd" {
		t.Fatalf("host=%q", gotHost)
	}
	want := httptest.NewRequest(http.MethodGet, "/", nil)
	want.SetBasicAuth("", "secret")
	if gotAuth != want.Header.Get("Authorization") {
		t.Fatalf("Authorization=%q want %q", gotAuth, want.Header.Get("Authorization"))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestMatchKey(t *testing.T) {
	if !MatchKey("secret", []string{"other", "secret"}) {
		t.Fatal("expected match")
	}
	if MatchKey("secre", []string{"secret"}) {
		t.Fatal("length mismatch must fail")
	}
	if MatchKey("", []string{"secret"}) {
		t.Fatal("empty must fail")
	}
}

func TestApplyIfBlobstore(t *testing.T) {
	t.Setenv("AUTH_KEY", "secret")
	t.Setenv("DISCOVERD", "")
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/x", nil)
	ApplyIfBlobstore(req)
	if req.Header.Get("Authorization") != "" {
		t.Fatal("must not authorize non-blobstore URLs")
	}
	req, _ = http.NewRequest(http.MethodGet, "http://blobstore.discoverd/x", nil)
	ApplyIfBlobstore(req)
	if _, pass, ok := req.BasicAuth(); !ok || pass != "secret" {
		t.Fatalf("blobstore URL basic=%v pass=%q", ok, pass)
	}
}

func TestCacheTokenMatchesReceiver(t *testing.T) {
	if CacheToken("app-1", "cluster-key") == CacheToken("app-1", "other") {
		t.Fatal("different keys")
	}
	if CacheToken("app-1", "k") == CacheToken("app-2", "k") {
		t.Fatal("different apps")
	}
}
