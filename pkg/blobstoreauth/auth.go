// Package blobstoreauth authenticates HTTP calls to the cluster blobstore.
//
// The blobstore accepts the cluster AUTH_KEY / CONTROLLER_KEY as HTTP Basic
// or Bearer, and an HMAC query token on per-app build-cache paths. Build jobs
// that hold a scoped controller token present it as Bearer; the server allows
// that credential only on slug/tarreceive upload paths.
package blobstoreauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/status"
)

const (
	// TokenPath is the root-only mount where gitreceive delivers a scoped
	// build token (Authorization: Bearer).
	TokenPath = "/run/secrets/controller_token"
	// KeyPath is the SEC-003 fallback file build.sh writes from CONTROLLER_KEY.
	KeyPath = "/run/secrets/controller_key"

	controllerLookupTimeout = 2 * time.Second
)

// EnvKeys returns AUTH_KEY then CONTROLLER_KEY, splitting comma-separated
// AUTH_KEY the same way the controller does.
func EnvKeys() []string {
	var keys []string
	seen := make(map[string]struct{})
	for _, env := range []string{"AUTH_KEY", "CONTROLLER_KEY"} {
		for _, k := range strings.Split(os.Getenv(env), ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	return keys
}

// EnvKey is the first configured cluster key, or "".
func EnvKey() string {
	if keys := EnvKeys(); len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// ClusterKey is the key used to authenticate to blobstore: env first, then
// (when DISCOVERD is set) controller service metadata. Discoverd is not
// consulted in unit tests so Apply stays side-effect free.
func ClusterKey() string {
	if k := EnvKey(); k != "" {
		return k
	}
	return lookupControllerKey()
}

func lookupControllerKey() string {
	d := os.Getenv("DISCOVERD")
	if d == "" || d == "none" {
		return ""
	}
	insts, err := discoverd.GetInstances("controller", controllerLookupTimeout)
	if err != nil || len(insts) == 0 {
		return ""
	}
	return strings.TrimSpace(insts[0].Meta["AUTH_KEY"])
}

// ApplyIfBlobstore calls Apply only when req targets blobstore.discoverd so
// artifact URIs that are not the cluster blobstore never receive the key.
func ApplyIfBlobstore(req *http.Request) {
	if req == nil || !IsBlobstoreURL(req.URL) {
		return
	}
	Apply(req)
}

// Apply sets Authorization from the scoped build token, cluster key env,
// fallback secret file, or (when DISCOVERD is set) controller metadata.
func Apply(req *http.Request) {
	if req == nil {
		return
	}
	if req.Header.Get("Authorization") != "" {
		return
	}
	if token := readSecret(TokenPath); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	if key := ClusterKey(); key != "" {
		req.SetBasicAuth("", key)
		return
	}
	if key := readSecret(KeyPath); key != "" {
		req.SetBasicAuth("", key)
	}
}

// MatchKey is a constant-time compare of got against any configured key.
func MatchKey(got string, keys []string) bool {
	if got == "" {
		return false
	}
	ok := false
	for _, k := range keys {
		if len(got) == len(k) && subtle.ConstantTimeCompare([]byte(got), []byte(k)) == 1 {
			ok = true
		}
	}
	return ok
}

// ApplyKey sets HTTP Basic authentication with the cluster key (empty user).
func ApplyKey(req *http.Request, key string) {
	if req == nil || key == "" {
		return
	}
	req.SetBasicAuth("", key)
}

// ApplyBearer sets Authorization: Bearer.
func ApplyBearer(req *http.Request, token string) {
	if req == nil || token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
}

func readSecret(path string) string {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// CacheToken is the HMAC-SHA256 hex token used on BUILD_CACHE_URL.
func CacheToken(appID, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}

// CacheAppID returns the app ID encoded in a signed cache path
// (/{appID}-cache.tgz or /{appID}-docker-cache.tgz).
func CacheAppID(path string) (string, bool) {
	base := strings.TrimPrefix(path, "/")
	if base == "" || strings.Contains(base, "/") {
		return "", false
	}
	for _, suffix := range []string{"-docker-cache.tgz", "-cache.tgz"} {
		if strings.HasSuffix(base, suffix) {
			id := strings.TrimSuffix(base, suffix)
			if id != "" {
				return id, true
			}
		}
	}
	return "", false
}

// ValidCacheToken reports whether token is the HMAC of the app ID in path
// under any of keys.
func ValidCacheToken(path, token string, keys []string) bool {
	if token == "" || len(keys) == 0 {
		return false
	}
	appID, ok := CacheAppID(path)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(token)
	if err != nil {
		return false
	}
	for _, key := range keys {
		mac := hmac.New(sha256.New, []byte(key))
		mac.Write([]byte(appID))
		if hmac.Equal(got, mac.Sum(nil)) {
			return true
		}
	}
	return false
}

// IsPublicPath is reachable without credentials (health/status).
func IsPublicPath(path, method string) bool {
	if path == status.Path {
		return true
	}
	return path == "/" && strings.EqualFold(method, http.MethodHead)
}

// IsBuildUploadPath is a slug or tarreceive object path that a scoped
// build:artifacts token may GET/HEAD/PUT.
func IsBuildUploadPath(path string) bool {
	return strings.HasPrefix(path, "/slugs/") || strings.HasPrefix(path, "/tarreceive/")
}

// IsBlobstoreHost reports a blobstore.discoverd request host (with or without
// a port). Layer downloads and backups use this host.
func IsBlobstoreHost(host string) bool {
	h := strings.ToLower(host)
	if i := strings.IndexByte(h, ':'); i >= 0 {
		h = h[:i]
	}
	return h == "blobstore.discoverd"
}

// IsBlobstoreURL reports whether u points at the cluster blobstore.
func IsBlobstoreURL(u *url.URL) bool {
	return u != nil && IsBlobstoreHost(u.Host)
}

// Transport injects blobstore credentials on requests to blobstore.discoverd.
type Transport struct {
	Base http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if req != nil && IsBlobstoreURL(req.URL) {
		req = req.Clone(req.Context())
		Apply(req)
	}
	return base.RoundTrip(req)
}
