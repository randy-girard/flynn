package main

import (
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
)

// allowUnauthenticatedEnv names the explicit opt-in to serve objects with no
// credentials when the release has no key.
const allowUnauthenticatedEnv = "BLOBSTORE_ALLOW_UNAUTHENTICATED"

// protect requires cluster-key or scoped-token credentials on blobstore
// object routes. Health/status stays public. Signed BUILD_CACHE_URL tokens
// are accepted only on that app's cache objects, expire, and are split into a
// read token (GET/HEAD) and a write token (PUT). Copy-From is
// refused for every credential except the cluster key and cluster-admin tokens.
// A scoped build token reaches only its own app's /slugs/apps/<app>/ and
// /tarreceive/apps/<app>/ objects, and may not PUT over a content-addressed
// layer or manifest that already exists (exists reports that; nil means the
// store holds nothing).
//
// With no cluster key and no token key, protect fails closed: every object
// request is 401 unless BLOBSTORE_ALLOW_UNAUTHENTICATED=1 (or true) is set.
func protect(inner http.Handler, keys []string, tokens *authorizer.Authorizer, exists func(string) (bool, error)) http.Handler {
	if tokens == nil && len(keys) == 0 {
		if allowUnauthenticated() {
			log.Printf("warning: blobstore authentication disabled by %s=1; every object is readable, writable and deletable without credentials", allowUnauthenticatedEnv)
			return inner
		}
		// Fail closed: with no key nothing can authenticate, so every
		// object request is refused. Status stays public.
		log.Printf("error: blobstore has no AUTH_KEY, CONTROLLER_KEY or ACCESS_TOKEN_KEY; refusing every object request (set the keys, or %s=1 to serve without authentication)", allowUnauthenticatedEnv)
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if blobstoreauth.IsPublicPath(path.Clean(req.URL.Path), req.Method) {
				inner.ServeHTTP(w, req)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="blobstore"`)
			http.Error(w, "Unauthorized: blobstore has no authentication key configured", http.StatusUnauthorized)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := path.Clean(req.URL.Path)
		if blobstoreauth.IsPublicPath(p, req.Method) {
			inner.ServeHTTP(w, req)
			return
		}
		if allowCacheToken(req, p, keys) {
			if hasCopyFrom(req) {
				// The cache token authorizes one object, not a read of
				// an arbitrary copy source.
				http.Error(w, "Forbidden: Blobstore-Copy-From requires the cluster key", http.StatusForbidden)
				return
			}
			inner.ServeHTTP(w, req)
			return
		}
		// authorizer treats Authorization: Bearer as a JWT. Also accept
		// the raw cluster key as Bearer, matching controller-style clients.
		if auth := req.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			if blobstoreauth.MatchKey(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")), keys) {
				inner.ServeHTTP(w, req)
				return
			}
		}
		if tokens != nil {
			tok, err := tokens.AuthorizeRequest(req)
			if err == nil && allowToken(tok, req.Method, p) {
				if hasCopyFrom(req) && !tok.HasClusterAdmin() {
					http.Error(w, "Forbidden: Blobstore-Copy-From requires the cluster key", http.StatusForbidden)
					return
				}
				if req.Method == http.MethodPut && !tok.HasClusterAdmin() && blobstoreauth.IsContentAddressedPath(p) {
					if code, msg := refuseOverwrite(p, exists); code != 0 {
						http.Error(w, msg, code)
						return
					}
				}
				inner.ServeHTTP(w, req)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="blobstore"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

// allowUnauthenticated reports the explicit opt-in to an open blobstore. Only
// 1 or true (any case) count; anything else keeps the blobstore closed.
func allowUnauthenticated() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(allowUnauthenticatedEnv))) {
	case "1", "true":
		return true
	}
	return false
}

// hasCopyFrom reports a Blobstore-Copy-From header (any value, any repeat).
// The blobstore reads the copy source without a second authorization check,
// so only the cluster key or a cluster-admin token may use it; scoped build
// and cache credentials would otherwise read any object.
func hasCopyFrom(req *http.Request) bool {
	return len(req.Header.Values("Blobstore-Copy-From")) > 0
}

func allowCacheToken(req *http.Request, p string, keys []string) bool {
	q := req.URL.Query()
	return blobstoreauth.ValidCacheRequest(req.Method, p, q.Get("token"), q.Get("exp"), keys, time.Now())
}

func allowToken(tok *authorizer.Token, method, p string) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	if !authz.TarreceiveAllowed(tok) {
		return false
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut:
		return blobstoreauth.BuildPathAllowed(p, grantAppIDs(tok))
	default:
		return false
	}
}

// grantAppIDs is the app IDs a scoped token is bound to.
func grantAppIDs(tok *authorizer.Token) []string {
	ids := make([]string, 0, len(tok.AppGrants))
	for _, g := range tok.AppGrants {
		ids = append(ids, g.AppID)
	}
	return ids
}

// refuseOverwrite returns a non-zero status when a scoped PUT to the
// content-addressed path p must not run: 409 when the object exists, 503
// when the store cannot say (fail closed).
func refuseOverwrite(p string, exists func(string) (bool, error)) (int, string) {
	if exists == nil {
		return 0, ""
	}
	found, err := exists(p)
	if err != nil {
		log.Println("error checking for existing object:", err)
		return http.StatusServiceUnavailable, "Service Unavailable"
	}
	if found {
		return http.StatusConflict, "Conflict: content-addressed object already exists"
	}
	return 0, ""
}

func clusterAuthorizer(keys []string) *authorizer.Authorizer {
	tokenKey, err := authorizer.ParseTokenKey(os.Getenv("ACCESS_TOKEN_KEY"))
	if err != nil {
		log.Println("warning: ignoring invalid ACCESS_TOKEN_KEY:", err)
		tokenKey = nil
	}
	maxValidity, err := authorizer.ParseTokenMaxValidity(os.Getenv("ACCESS_TOKEN_MAX_VALIDITY"))
	if err != nil {
		log.Println("warning: ignoring invalid ACCESS_TOKEN_MAX_VALIDITY:", err)
		maxValidity = 0
	}
	if len(keys) == 0 && tokenKey == nil {
		return nil
	}
	return authorizer.New(keys, nil, tokenKey, maxValidity)
}
