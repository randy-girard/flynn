// Package blobstoreauth authenticates HTTP calls to the cluster blobstore.
//
// The blobstore accepts the cluster AUTH_KEY / CONTROLLER_KEY as HTTP Basic
// or Bearer, and an HMAC query token on per-app build-cache paths. Build jobs
// that hold a scoped controller token present it as Bearer; the server allows
// that credential only on its own app's /slugs/apps/<app>/ and
// /tarreceive/apps/<app>/ objects (BuildPathAllowed).
package blobstoreauth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
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

// CacheOp is the operation a cache token authorizes.
type CacheOp string

const (
	// CacheRead authorizes GET and HEAD of one cache object.
	CacheRead CacheOp = "read"
	// CacheWrite authorizes PUT of one cache object.
	CacheWrite CacheOp = "write"
)

// CacheTokenTTL is how long a signed cache URL stays valid. It covers the
// build timeout (at most 30 minutes) plus time spent waiting for a host.
const CacheTokenTTL = 2 * time.Hour

// cacheKeyInfo is the HKDF context for cache token signing keys, so a cache
// token is never an HMAC under the raw cluster key.
const cacheKeyInfo = "flynn blobstore build cache token v1"

func cacheSigningKey(key string) []byte {
	k, err := hkdf.Key(sha256.New, []byte(key), nil, cacheKeyInfo, sha256.Size)
	if err != nil {
		// Only possible for an over-long output length.
		panic(err)
	}
	return k
}

func cacheMAC(signingKey []byte, appID, name string, op CacheOp, exp int64) []byte {
	mac := hmac.New(sha256.New, signingKey)
	fmt.Fprintf(mac, "%s|%s|%s|%d", appID, name, op, exp)
	return mac.Sum(nil)
}

// CacheToken is the hex HMAC-SHA256 over appID|name|op|exp, keyed by a key
// derived from the cluster key with HKDF. name is "cache" or "docker-cache".
func CacheToken(appID, name string, op CacheOp, exp time.Time, key string) string {
	return hex.EncodeToString(cacheMAC(cacheSigningKey(key), appID, name, op, exp.Unix()))
}

// SignedCacheURL returns base/{appID}-{name}.tgz with the token and exp query
// parameters ValidCacheRequest checks.
func SignedCacheURL(base, appID, name string, op CacheOp, exp time.Time, key string) string {
	return fmt.Sprintf("%s/%s-%s.tgz?token=%s&exp=%d", base, appID, name, CacheToken(appID, name, op, exp, key), exp.Unix())
}

// CacheObject returns the app ID and object name ("cache" or "docker-cache")
// encoded in a signed cache path (/{appID}-cache.tgz or
// /{appID}-docker-cache.tgz).
func CacheObject(path string) (appID, name string, ok bool) {
	base := strings.TrimPrefix(path, "/")
	if base == "" || strings.Contains(base, "/") {
		return "", "", false
	}
	for _, o := range []struct{ suffix, name string }{
		{"-docker-cache.tgz", "docker-cache"},
		{"-cache.tgz", "cache"},
	} {
		if strings.HasSuffix(base, o.suffix) {
			id := strings.TrimSuffix(base, o.suffix)
			if id != "" {
				return id, o.name, true
			}
		}
	}
	return "", "", false
}

// CacheAppID returns the app ID encoded in a signed cache path.
func CacheAppID(path string) (string, bool) {
	id, _, ok := CacheObject(path)
	return id, ok
}

// ValidCacheRequest reports whether token and exp authorize method on the
// cache object at path under any of keys at time now. GET and HEAD need a read
// token, PUT needs a write token, and nothing else is allowed.
func ValidCacheRequest(method, path, token, exp string, keys []string, now time.Time) bool {
	if token == "" || exp == "" || len(keys) == 0 {
		return false
	}
	var op CacheOp
	switch method {
	case http.MethodGet, http.MethodHead:
		op = CacheRead
	case http.MethodPut:
		op = CacheWrite
	default:
		return false
	}
	appID, name, ok := CacheObject(path)
	if !ok {
		return false
	}
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || !now.Before(time.Unix(expUnix, 0)) {
		return false
	}
	got, err := hex.DecodeString(token)
	if err != nil {
		return false
	}
	valid := false
	for _, key := range keys {
		if hmac.Equal(got, cacheMAC(cacheSigningKey(key), appID, name, op, expUnix)) {
			valid = true
		}
	}
	return valid
}

// IsPublicPath is reachable without credentials (health/status).
func IsPublicPath(path, method string) bool {
	if path == status.Path {
		return true
	}
	return path == "/" && strings.EqualFold(method, http.MethodHead)
}

// baseURL is the blobstore root every cluster component addresses.
const baseURL = "http://blobstore.discoverd"

// appsDir is the directory under /slugs and /tarreceive that holds one
// sub-directory per app. A build token reaches only its own app's
// sub-directory (BuildPathAllowed).
const appsDir = "apps"

// SlugLayerURL is where slugbuilder stores a slug layer. With an app ID it is
// that app's own prefix (/slugs/apps/<app>/layers/<sha>.squashfs); with none
// it is the legacy shared directory, which only the cluster key may write.
func SlugLayerURL(appID, sha string) string {
	return baseURL + appPrefix("slugs", appID) + "/layers/" + sha + ".squashfs"
}

// SlugImageURL is where slugbuilder stores a slug image manifest.
func SlugImageURL(appID, id string) string {
	return baseURL + appPrefix("slugs", appID) + "/images/" + id + ".json"
}

// SlugLayerURLTemplate is the LayerURLTemplate of a slug artifact.
func SlugLayerURLTemplate(appID string) string {
	return baseURL + appPrefix("slugs", appID) + "/layers/{id}.squashfs"
}

// TarreceiveImageURL is where tarreceive or a dockerbuilder build stores an
// image manifest. Its layers are in /tarreceive/apps/<app>/layers (or, for
// older images and cluster-key pushes that name no app, the shared
// /tarreceive/layers store), which only the tarreceive service writes.
func TarreceiveImageURL(appID, id string) string {
	return baseURL + appPrefix("tarreceive", appID) + "/images/" + id + ".json"
}

func appPrefix(root, appID string) string {
	if appID == "" {
		return "/" + root
	}
	return "/" + root + "/" + appsDir + "/" + appID
}

// BuildPathAllowed reports whether a scoped build:artifacts token whose app
// grants are appIDs may GET, HEAD or PUT the cleaned object path p. Only that
// app's own objects qualify: /slugs/apps/<app>/{layers,images}/<name> and
// /tarreceive/apps/<app>/images/<name>. The legacy shared /slugs and
// /tarreceive directories and every other app's prefix are cluster-key only.
func BuildPathAllowed(p string, appIDs []string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) != 5 || parts[1] != appsDir || parts[2] == "" || parts[4] == "" || parts[4] == "." || parts[4] == ".." {
		return false
	}
	switch {
	case parts[0] == "slugs" && (parts[3] == "layers" || parts[3] == "images"):
	case parts[0] == "tarreceive" && parts[3] == "images":
	default:
		return false
	}
	for _, id := range appIDs {
		if id != "" && id == parts[2] {
			return true
		}
	}
	return false
}

// IsContentAddressedPath reports an image layer or manifest object, which is
// named by the hash of its content. A scoped credential never needs to
// replace one, so the blobstore refuses a scoped PUT over an existing one.
func IsContentAddressedPath(p string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	var dir string
	switch {
	case len(parts) == 3:
		dir = parts[1]
	case len(parts) == 5 && parts[1] == appsDir && parts[2] != "":
		dir = parts[3]
	default:
		return false
	}
	if parts[0] != "slugs" && parts[0] != "tarreceive" {
		return false
	}
	return (dir == "layers" || dir == "images") && parts[len(parts)-1] != ""
}

// sharedLayerPath is a layer in the shared tarreceive store. tarreceive
// writes these itself, after checking the tar hash, so a build artifact may
// reference them.
func sharedLayerPath(p string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	return len(parts) == 3 && parts[0] == "tarreceive" && parts[1] == "layers" && parts[2] != "" && parts[2] != "." && parts[2] != ".."
}

// appLayerPath is a layer tarreceive stored under one of appIDs' own
// prefixes, /tarreceive/apps/<app>/layers/<name> (EDGE-35). Like the shared
// store only tarreceive writes there, so a build artifact may reference it,
// but a build token may not PUT it (BuildPathAllowed).
func appLayerPath(p string, appIDs []string) bool {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) != 5 || parts[0] != "tarreceive" || parts[1] != appsDir || parts[3] != "layers" || parts[4] == "" || parts[4] == "." || parts[4] == ".." {
		return false
	}
	for _, id := range appIDs {
		if id != "" && id == parts[2] {
			return true
		}
	}
	return false
}

// BuildArtifactURLAllowed reports whether an artifact URL registered by a
// scoped build token may be stored. Hosts fetch artifact layers with the
// cluster key, so a URL outside the app's own prefix would read another
// app's blobs. Only plain http URLs on the cluster blobstore qualify, with a
// path that is already clean, no query, inside the app's own prefix (its
// tarreceive layers included) or the shared tarreceive layer store.
func BuildArtifactURLAllowed(raw string, appIDs []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host != "blobstore.discoverd" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if u.RawPath != "" || path.Clean(u.Path) != u.Path {
		return false
	}
	return BuildPathAllowed(u.Path, appIDs) || sharedLayerPath(u.Path) || appLayerPath(u.Path, appIDs)
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
