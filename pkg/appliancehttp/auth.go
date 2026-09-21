// Package appliancehttp authenticates datastore appliance admin HTTP APIs.
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
