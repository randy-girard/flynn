package main

import (
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
)

// protect requires cluster-key or scoped-token credentials on blobstore
// object routes. Health/status stays public. Signed BUILD_CACHE_URL tokens
// are accepted only on that app's cache objects (GET/HEAD/PUT).
func protect(inner http.Handler, keys []string, tokens *authorizer.Authorizer) http.Handler {
	if tokens == nil && len(keys) == 0 {
		log.Println("warning: blobstore authentication disabled (set AUTH_KEY)")
		return inner
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := path.Clean(req.URL.Path)
		if blobstoreauth.IsPublicPath(p, req.Method) {
			inner.ServeHTTP(w, req)
			return
		}
		if allowCacheToken(req, p, keys) {
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
				inner.ServeHTTP(w, req)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="blobstore"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func allowCacheToken(req *http.Request, p string, keys []string) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut:
	default:
		return false
	}
	return blobstoreauth.ValidCacheToken(p, req.URL.Query().Get("token"), keys)
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
		return blobstoreauth.IsBuildUploadPath(p)
	default:
		return false
	}
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
