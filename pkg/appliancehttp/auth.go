// Package appliancehttp authenticates datastore appliance admin HTTP APIs and
// the other internal system APIs that take the cluster key (logaggregator,
// router).
package appliancehttp

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
)

// Key is the secret appliance admin HTTP APIs accept as HTTP basic password
// or Authorization: Bearer. CONTROLLER_KEY is preferred; AUTH_KEY is the
// controller-worker name for the same cluster secret.
func Key() string {
	for _, env := range []string{"CONTROLLER_KEY", "AUTH_KEY"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	return ""
}

// RequestKey extracts a presented credential from Bearer, Basic, or Auth-Key.
func RequestKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	if a := r.Header.Get("Authorization"); len(a) >= 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	return strings.TrimSpace(r.Header.Get("Auth-Key"))
}

// Authorized reports whether r presents key. An empty configured key never
// authenticates (fail closed).
func Authorized(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	got := RequestKey(r)
	if got == "" || len(got) != len(key) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(key)) == 1
}

// PublicStatusPath is the unauthenticated health URL. GET /status and every
// other appliance route require Key.
func PublicStatusPath(path string) bool {
	return path == status.Path
}

// SetAuth attaches the appliance key as HTTP basic (empty user, key password),
// matching pkg/httpclient.Client.Key.
func SetAuth(req *http.Request, key string) {
	if req == nil || key == "" {
		return
	}
	req.SetBasicAuth("", key)
}

// Unauthorized writes a 401 JSON error.
func Unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="flynn-appliance"`)
	httphelper.Error(w, httphelper.JSONError{
		Code:    httphelper.UnauthorizedErrorCode,
		Message: "authentication required",
	})
}

// Keys is every cluster key this process accepts: CONTROLLER_KEY, then each
// comma-separated entry of AUTH_KEY (the controller keeps a list there so the
// key can rotate). Empty entries are dropped; nil when neither is set.
func Keys() []string {
	var keys []string
	for _, env := range []string{"CONTROLLER_KEY", "AUTH_KEY"} {
		for _, k := range strings.Split(os.Getenv(env), ",") {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// ClientKey is the key a system process sends to another system API: the
// first of Keys.
func ClientKey() string {
	if keys := Keys(); len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// AuthorizedAny reports whether r presents one of keys. No keys never
// authenticates.
func AuthorizedAny(r *http.Request, keys []string) bool {
	for _, k := range keys {
		if Authorized(r, k) {
			return true
		}
	}
	return false
}

// RequireKeys serves h only to requests that present one of keys; the health
// check (status.Path) stays public. With no keys every other request gets
// 401, so a release that lost its key fails closed.
func RequireKeys(keys []string, h http.Handler) http.Handler {
	keys = append([]string(nil), keys...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if PublicStatusPath(r.URL.Path) || AuthorizedAny(r, keys) {
			h.ServeHTTP(w, r)
			return
		}
		Unauthorized(w)
	})
}
