package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/tokensigner"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func authServer(t *testing.T, keys []string, tokens *authorizer.Authorizer) *httptest.Server {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Inner", r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	})
	return httptest.NewServer(protect(inner, keys, tokens))
}

func TestProtectRejectsUnauthenticatedObjectMethods(t *testing.T) {
	srv := authServer(t, []string{"secret"}, authorizer.New([]string{"secret"}, nil, nil, 0))
	defer srv.Close()

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req, err := http.NewRequest(method, srv.URL+"/file.txt", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated = %d, want 401", method, res.StatusCode)
		}
	}
}

func TestProtectAcceptsClusterKeyBasicAndBearer(t *testing.T) {
	keys := []string{"secret"}
	srv := authServer(t, keys, authorizer.New(keys, nil, nil, 0))
	defer srv.Close()

	for _, setup := range []func(*http.Request){
		func(req *http.Request) { req.SetBasicAuth("", "secret") },
		func(req *http.Request) { req.Header.Set("Authorization", "Bearer secret") },
	} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequest(method, srv.URL+"/file.txt", strings.NewReader("x"))
			if err != nil {
				t.Fatal(err)
			}
			setup(req)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("%s with cluster key = %d", method, res.StatusCode)
			}
		}
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/file.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("", "wrong")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong key = %d", res.StatusCode)
	}
}

// cacheQuery is the query string of a signed cache URL for app-1's tarball.
func cacheQuery(key string, op blobstoreauth.CacheOp, exp time.Time) string {
	u := blobstoreauth.SignedCacheURL("", "app-1", "cache", op, exp, key)
	return u[strings.Index(u, "?"):]
}

func TestProtectSignedCacheURL(t *testing.T) {
	key := "cluster-key"
	srv := authServer(t, []string{key}, authorizer.New([]string{key}, nil, nil, 0))
	defer srv.Close()

	exp := time.Now().Add(time.Hour)
	readQ := cacheQuery(key, blobstoreauth.CacheRead, exp)
	writeQ := cacheQuery(key, blobstoreauth.CacheWrite, exp)
	do := func(method, path string) int {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	for _, c := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/app-1-cache.tgz" + readQ, http.StatusOK},
		{http.MethodHead, "/app-1-cache.tgz" + readQ, http.StatusOK},
		{http.MethodPut, "/app-1-cache.tgz" + writeQ, http.StatusOK},
		// BUILD-07: read and write are separate capabilities.
		{http.MethodPut, "/app-1-cache.tgz" + readQ, http.StatusUnauthorized},
		{http.MethodGet, "/app-1-cache.tgz" + writeQ, http.StatusUnauthorized},
		// Tampered token.
		{http.MethodGet, "/app-1-cache.tgz" + readQ + "ff", http.StatusUnauthorized},
		// Valid token on another app's cache.
		{http.MethodGet, "/app-2-cache.tgz" + readQ, http.StatusUnauthorized},
		// Cache token cannot DELETE or read non-cache paths.
		{http.MethodDelete, "/app-1-cache.tgz" + writeQ, http.StatusUnauthorized},
		{http.MethodGet, "/slugs/layers/x.squashfs" + readQ, http.StatusUnauthorized},
		// The old unsigned-expiry token no longer works.
		{http.MethodGet, "/app-1-cache.tgz?token=" + legacyCacheToken("app-1", key), http.StatusUnauthorized},
	} {
		if got := do(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, got, c.want)
		}
	}
}

// BUILD-07: a cache URL stops working at its expiry.
func TestProtectExpiredCacheURL(t *testing.T) {
	key := "cluster-key"
	srv := authServer(t, []string{key}, authorizer.New([]string{key}, nil, nil, 0))
	defer srv.Close()

	for _, op := range []blobstoreauth.CacheOp{blobstoreauth.CacheRead, blobstoreauth.CacheWrite} {
		method := http.MethodGet
		if op == blobstoreauth.CacheWrite {
			method = http.MethodPut
		}
		q := cacheQuery(key, op, time.Now().Add(-time.Minute))
		req, err := http.NewRequest(method, srv.URL+"/app-1-cache.tgz"+q, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expired %s cache URL = %d, want 401", op, res.StatusCode)
		}
	}
}

func legacyCacheToken(appID, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestProtectPublicEndpoints(t *testing.T) {
	srv := authServer(t, []string{"secret"}, authorizer.New([]string{"secret"}, nil, nil, 0))
	defer srv.Close()

	res, err := http.Head(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("HEAD / = %d", res.StatusCode)
	}

	res, err = http.Get(srv.URL + status.Path)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	res, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET / listing must require auth, got %d", res.StatusCode)
	}
}

func TestProtectBuildTokenPaths(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	signer := tokensigner.New(priv)
	now := time.Now()
	token, err := signer.Sign(&api.AccessToken{
		UserEmail:  "build:myapp",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants: []*api.AppGrant{
			{AppId: "app-1", Permissions: []string{"app:write"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	pk, err := authorizer.ParseTokenKey(base64.URLEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	tokens := authorizer.New([]string{"secret"}, nil, pk, time.Hour)
	srv := authServer(t, []string{"secret"}, tokens)
	defer srv.Close()

	do := func(method, path string) int {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if got := do(http.MethodPut, "/slugs/layers/abc.squashfs"); got != http.StatusOK {
		t.Fatalf("build PUT slug = %d", got)
	}
	if got := do(http.MethodPut, "/tarreceive/images/x.json"); got != http.StatusOK {
		t.Fatalf("build PUT tarreceive = %d", got)
	}
	if got := do(http.MethodGet, "/repos/app.tar"); got != http.StatusUnauthorized {
		t.Fatalf("build GET repo = %d", got)
	}
	if got := do(http.MethodDelete, "/slugs/layers/abc.squashfs"); got != http.StatusUnauthorized {
		t.Fatalf("build DELETE = %d", got)
	}
}

func TestProtectDisabledWithoutKeys(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(protect(inner, nil, nil))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("no keys configured should fail-open, got %d", res.StatusCode)
	}
}

func copyFromStatus(t *testing.T, srv *httptest.Server, path string, setup func(*http.Request), values ...string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		req.Header.Add("Blobstore-Copy-From", v)
	}
	setup(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// A signed BUILD_CACHE_URL token is scoped to one app's cache object. It must
// not be able to name any other object as a copy source and read it back.
func TestProtectCacheTokenRejectsCopyFrom(t *testing.T) {
	key := "cluster-key"
	srv := authServer(t, []string{key}, authorizer.New([]string{key}, nil, nil, 0))
	defer srv.Close()

	q := cacheQuery(key, blobstoreauth.CacheWrite, time.Now().Add(time.Hour))
	for _, src := range []string{"/repos/victim.tar", "/app-2-cache.tgz", "/slugs/layers/x.squashfs"} {
		got := copyFromStatus(t, srv, "/app-1-cache.tgz"+q, func(*http.Request) {}, src)
		if got < 400 {
			t.Fatalf("cache token Copy-From %s = %d, want >= 400", src, got)
		}
	}
	// Plain PUT with the same token still works.
	if got := copyFromStatus(t, srv, "/app-1-cache.tgz"+q, func(*http.Request) {}); got != http.StatusOK {
		t.Fatalf("cache token plain PUT = %d", got)
	}
}

func TestProtectBuildTokenRejectsCopyFrom(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token, err := tokensigner.New(priv).Sign(&api.AccessToken{
		UserEmail:  "build:myapp",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants:  []*api.AppGrant{{AppId: "app-1", Permissions: []string{"app:write"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pk, err := authorizer.ParseTokenKey(base64.URLEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	srv := authServer(t, []string{"secret"}, authorizer.New([]string{"secret"}, nil, pk, time.Hour))
	defer srv.Close()

	bearer := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) }
	for _, src := range []string{"/repos/victim.tar", "/slugs/layers/other.squashfs", "/tarreceive/images/x.json"} {
		if got := copyFromStatus(t, srv, "/slugs/layers/x.squashfs", bearer, src); got < 400 {
			t.Fatalf("build token Copy-From %s = %d, want >= 400", src, got)
		}
	}
	// A second Copy-From value must not slip past a check on the first.
	if got := copyFromStatus(t, srv, "/slugs/layers/x.squashfs", bearer, "", "/repos/victim.tar"); got < 400 {
		t.Fatalf("build token multi-value Copy-From = %d, want >= 400", got)
	}
	if got := copyFromStatus(t, srv, "/slugs/layers/x.squashfs", bearer); got != http.StatusOK {
		t.Fatalf("build token plain PUT = %d", got)
	}
}

// The cluster key (operators, backups) keeps Copy-From.
func TestProtectClusterKeyMayCopyFrom(t *testing.T) {
	keys := []string{"secret"}
	srv := authServer(t, keys, authorizer.New(keys, nil, nil, 0))
	defer srv.Close()
	for _, setup := range []func(*http.Request){
		func(req *http.Request) { req.SetBasicAuth("", "secret") },
		func(req *http.Request) { req.Header.Set("Authorization", "Bearer secret") },
	} {
		if got := copyFromStatus(t, srv, "/dst.txt", setup, "/src.txt"); got != http.StatusOK {
			t.Fatalf("cluster key Copy-From = %d", got)
		}
	}
}
