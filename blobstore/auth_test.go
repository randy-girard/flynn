package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
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

func TestProtectSignedCacheURL(t *testing.T) {
	key := "cluster-key"
	srv := authServer(t, []string{key}, authorizer.New([]string{key}, nil, nil, 0))
	defer srv.Close()

	tok := blobstoreauth.CacheToken("app-1", key)
	okURL := srv.URL + "/app-1-cache.tgz?token=" + tok
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req, err := http.NewRequest(method, okURL, strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("signed cache %s = %d", method, res.StatusCode)
		}
	}

	// Tampered token.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/app-1-cache.tgz?token="+tok+"ff", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tampered token = %d", res.StatusCode)
	}

	// Valid token on another app's cache.
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/app-2-cache.tgz?token="+tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-app cache token = %d", res.StatusCode)
	}

	// Cache token cannot DELETE or read non-cache paths.
	req, err = http.NewRequest(http.MethodDelete, okURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cache token DELETE = %d", res.StatusCode)
	}
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/slugs/layers/x.squashfs?token="+tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cache token on slug path = %d", res.StatusCode)
	}
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
