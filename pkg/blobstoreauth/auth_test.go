package blobstoreauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
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

func TestValidCacheRequest(t *testing.T) {
	key := "cluster-key"
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(time.Hour)
	read := CacheToken("app-1", "cache", CacheRead, exp, key)
	write := CacheToken("app-1", "cache", CacheWrite, exp, key)
	e := strconv.FormatInt(exp.Unix(), 10)
	keys := []string{key}

	if !ValidCacheRequest("GET", "/app-1-cache.tgz", read, e, keys, now) {
		t.Fatal("valid read token rejected")
	}
	if !ValidCacheRequest("HEAD", "/app-1-cache.tgz", read, e, keys, now) {
		t.Fatal("read token must cover HEAD")
	}
	if !ValidCacheRequest("PUT", "/app-1-cache.tgz", write, e, keys, now) {
		t.Fatal("valid write token rejected")
	}
	if ValidCacheRequest("PUT", "/app-1-cache.tgz", read, e, keys, now) {
		t.Fatal("read token must not authorize PUT")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", write, e, keys, now) {
		t.Fatal("write token must not authorize GET")
	}
	if ValidCacheRequest("DELETE", "/app-1-cache.tgz", write, e, keys, now) {
		t.Fatal("no cache token authorizes DELETE")
	}
	if ValidCacheRequest("GET", "/app-2-cache.tgz", read, e, keys, now) {
		t.Fatal("token must not authorize another app")
	}
	if ValidCacheRequest("GET", "/app-1-docker-cache.tgz", read, e, keys, now) {
		t.Fatal("token must not authorize the other cache object")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", read[:len(read)-2]+"00", e, keys, now) {
		t.Fatal("tampered token must fail")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", read, e, []string{"other-key"}, now) {
		t.Fatal("wrong key must fail")
	}
	later := strconv.FormatInt(exp.Add(time.Hour).Unix(), 10)
	if ValidCacheRequest("GET", "/app-1-cache.tgz", read, later, keys, now) {
		t.Fatal("extending exp must invalidate the signature")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", read, "", keys, now) {
		t.Fatal("missing exp must fail")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", read, "soon", keys, now) {
		t.Fatal("unparseable exp must fail")
	}
}

// BUILD-07: the cache token used to be HMAC(appID) with no expiry, so a copy
// leaked from a build log or buildpack worked forever.
func TestCacheTokenExpires(t *testing.T) {
	key := "cluster-key"
	now := time.Unix(1_700_000_000, 0)
	exp := now.Add(CacheTokenTTL)
	tok := CacheToken("app-1", "cache", CacheRead, exp, key)
	e := strconv.FormatInt(exp.Unix(), 10)
	if !ValidCacheRequest("GET", "/app-1-cache.tgz", tok, e, []string{key}, exp.Add(-time.Second)) {
		t.Fatal("token must be valid before exp")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", tok, e, []string{key}, exp) {
		t.Fatal("token must be invalid at exp")
	}
	if ValidCacheRequest("GET", "/app-1-cache.tgz", tok, e, []string{key}, exp.Add(24*time.Hour)) {
		t.Fatal("token must be invalid after exp")
	}
	if CacheTokenTTL <= 0 || CacheTokenTTL > 6*time.Hour {
		t.Fatalf("CacheTokenTTL = %s, want a bounded build-length lifetime", CacheTokenTTL)
	}
}

// The old token (plain HMAC-SHA256 of the app ID under the cluster key) is
// not derived from a subkey and carries no expiry; it must no longer work.
func TestLegacyCacheTokenRejected(t *testing.T) {
	key := "cluster-key"
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte("app-1"))
	legacy := hex.EncodeToString(mac.Sum(nil))
	now := time.Unix(1_700_000_000, 0)
	e := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	for _, ex := range []string{"", e} {
		if ValidCacheRequest("GET", "/app-1-cache.tgz", legacy, ex, []string{key}, now) {
			t.Fatalf("legacy token accepted with exp=%q", ex)
		}
	}
}

// The signing key is derived from the cluster key, so the raw cluster key
// HMAC of the same message must not verify.
func TestCacheTokenUsesDerivedKey(t *testing.T) {
	key := "cluster-key"
	exp := time.Unix(1_700_003_600, 0)
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "app-1|cache|read|%d", exp.Unix())
	raw := hex.EncodeToString(mac.Sum(nil))
	if CacheToken("app-1", "cache", CacheRead, exp, key) == raw {
		t.Fatal("token is signed with the raw cluster key, want an HKDF-derived key")
	}
}

func TestSignedCacheURL(t *testing.T) {
	exp := time.Unix(1_700_003_600, 0)
	u := SignedCacheURL("http://blobstore.discoverd", "app-1", "docker-cache", CacheWrite, exp, "k")
	pu, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if pu.Host != "blobstore.discoverd" || pu.Path != "/app-1-docker-cache.tgz" {
		t.Fatalf("url = %s", u)
	}
	q := pu.Query()
	if q.Get("exp") != "1700003600" {
		t.Fatalf("exp = %q", q.Get("exp"))
	}
	if !ValidCacheRequest("PUT", pu.Path, q.Get("token"), q.Get("exp"), []string{"k"}, exp.Add(-time.Minute)) {
		t.Fatal("signed URL does not verify")
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
}

// NEW-05: a build token reaches only its own app's prefix. The legacy shared
// /slugs/ and /tarreceive/ directories are cluster-key only.
func TestBuildPathAllowedIsPerApp(t *testing.T) {
	apps := []string{"app-1"}
	for _, p := range []string{
		"/slugs/apps/app-1/layers/abc.squashfs",
		"/slugs/apps/app-1/images/abc.json",
		"/tarreceive/apps/app-1/images/abc.json",
	} {
		if !BuildPathAllowed(p, apps) {
			t.Errorf("%s should be allowed for app-1", p)
		}
	}
	for _, p := range []string{
		"/slugs/apps/app-2/layers/abc.squashfs",
		"/tarreceive/apps/app-2/images/abc.json",
		"/slugs/layers/abc.squashfs",
		"/slugs/images/abc.json",
		"/tarreceive/images/x.json",
		"/tarreceive/layers/x.squashfs",
		"/tarreceive/apps/app-1/layers/x.squashfs",
		"/slugs/apps/app-1/other/x",
		"/slugs/apps/app-1/layers/",
		"/slugs/apps/app-1/layers/a/b",
		"/slugs/apps/app-1/layers/../../app-2/layers/x",
		"/slugs/apps/app-1",
		"/repos/x.tar",
		"/plugins/x",
		"/app-1-cache.tgz",
	} {
		if BuildPathAllowed(p, apps) {
			t.Errorf("%s must not be allowed for app-1", p)
		}
	}
	if BuildPathAllowed("/slugs/apps/app-1/layers/x", nil) {
		t.Error("no grants, no paths")
	}
	if BuildPathAllowed("/slugs/apps//layers/x", []string{""}) {
		t.Error("an empty app id grants nothing")
	}
}

// NEW-05: layer and image objects are named by their content hash, so a
// scoped credential never needs to replace one.
func TestIsContentAddressedPath(t *testing.T) {
	for _, p := range []string{
		"/slugs/layers/abc.squashfs",
		"/slugs/images/abc.json",
		"/tarreceive/layers/abc.squashfs",
		"/tarreceive/layers/abc.json",
		"/tarreceive/images/abc.json",
		"/slugs/apps/app-1/layers/abc.squashfs",
		"/slugs/apps/app-1/images/abc.json",
		"/tarreceive/apps/app-1/images/abc.json",
	} {
		if !IsContentAddressedPath(p) {
			t.Errorf("%s is content addressed", p)
		}
	}
	for _, p := range []string{"/app-1-cache.tgz", "/repos/app-1.tar", "/slugs/x", "/backups/x.tar"} {
		if IsContentAddressedPath(p) {
			t.Errorf("%s is not content addressed", p)
		}
	}
}

func TestAppBlobURLs(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{SlugLayerURL("app-1", "abc"), "http://blobstore.discoverd/slugs/apps/app-1/layers/abc.squashfs"},
		{SlugImageURL("app-1", "abc"), "http://blobstore.discoverd/slugs/apps/app-1/images/abc.json"},
		{SlugLayerURLTemplate("app-1"), "http://blobstore.discoverd/slugs/apps/app-1/layers/{id}.squashfs"},
		{TarreceiveImageURL("app-1", "abc"), "http://blobstore.discoverd/tarreceive/apps/app-1/images/abc.json"},
		// No app id (an operator run with the cluster key): the legacy layout.
		{SlugLayerURL("", "abc"), "http://blobstore.discoverd/slugs/layers/abc.squashfs"},
		{SlugImageURL("", "abc"), "http://blobstore.discoverd/slugs/images/abc.json"},
		{SlugLayerURLTemplate(""), "http://blobstore.discoverd/slugs/layers/{id}.squashfs"},
		{TarreceiveImageURL("", "abc"), "http://blobstore.discoverd/tarreceive/images/abc.json"},
	} {
		if c.got != c.want {
			t.Errorf("got %s, want %s", c.got, c.want)
		}
	}
}

// NEW-05: an artifact a build token registers is fetched by hosts with the
// cluster key, so every URL in it must stay in the app's own prefix (or the
// shared tarreceive layer store, which only tarreceive writes).
func TestBuildArtifactURLAllowed(t *testing.T) {
	apps := []string{"app-1"}
	for _, u := range []string{
		"http://blobstore.discoverd/slugs/apps/app-1/images/abc.json",
		"http://blobstore.discoverd/slugs/apps/app-1/layers/abc.squashfs",
		"http://blobstore.discoverd/tarreceive/apps/app-1/images/abc.json",
		"http://blobstore.discoverd/tarreceive/layers/abc.squashfs",
		// EDGE-35: tarreceive stores the app's own layers here
		"http://blobstore.discoverd/tarreceive/apps/app-1/layers/abc.squashfs",
	} {
		if !BuildArtifactURLAllowed(u, apps) {
			t.Errorf("%s should be allowed", u)
		}
	}
	for _, u := range []string{
		"http://blobstore.discoverd/slugs/apps/app-2/images/abc.json",
		"http://blobstore.discoverd/slugs/layers/abc.squashfs",
		"http://blobstore.discoverd/slugs/images/abc.json",
		"http://blobstore.discoverd/tarreceive/images/abc.json",
		"http://blobstore.discoverd/repos/app-2.tar",
		"http://blobstore.discoverd/slugs/apps/app-1/layers/..%2F..%2Fapp-2%2Flayers%2Fx.squashfs",
		"http://blobstore.discoverd/slugs/apps/app-1/../app-2/layers/x.squashfs",
		"http://BLOBSTORE.discoverd:80/slugs/apps/app-2/layers/x.squashfs",
		"http://blobstore.discoverd/tarreceive/layers/a/b.squashfs",
		"http://blobstore.discoverd/tarreceive/apps/app-2/layers/abc.squashfs",
		"http://blobstore.discoverd/tarreceive/apps/app-1/layers/a/b.squashfs",
		"http://blobstore.discoverd/tarreceive/apps/app-1/other/abc.squashfs",
		"http://blobstore.discoverd/slugs/apps/app-1/layers/x.squashfs?token=1",
		"file:///var/lib/flynn/layer-cache/x.squashfs",
		"https://example.com/x.squashfs",
		"http://example.com/x.squashfs",
		"",
		"::",
	} {
		if BuildArtifactURLAllowed(u, apps) {
			t.Errorf("%q must not be allowed", u)
		}
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
	exp := time.Unix(1_700_003_600, 0)
	base := CacheToken("app-1", "cache", CacheRead, exp, "cluster-key")
	if base == CacheToken("app-1", "cache", CacheRead, exp, "other") {
		t.Fatal("different keys")
	}
	if base == CacheToken("app-2", "cache", CacheRead, exp, "cluster-key") {
		t.Fatal("different apps")
	}
	if base == CacheToken("app-1", "docker-cache", CacheRead, exp, "cluster-key") {
		t.Fatal("different objects")
	}
	if base == CacheToken("app-1", "cache", CacheWrite, exp, "cluster-key") {
		t.Fatal("different operations")
	}
	if base == CacheToken("app-1", "cache", CacheRead, exp.Add(time.Second), "cluster-key") {
		t.Fatal("different expiry")
	}
}
