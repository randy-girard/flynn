// Package pluginui is the shared verifier for the dashboard SSO token that
// every plugin dashboard UI receives, and the permission checks plugin UIs
// use for writes. Plugins import it instead of keeping their own copy, so the
// checks are fixed in one place.
//
// The dashboard (flynn-plugin-dashboard) mints one ES256 JWT per proxied
// request with iss=flynn-dashboard, aud=flynn-plugin-ui, a short exp, the
// target plugin name in "plugin", and the signing key id in the header. A
// plugin must accept only tokens minted for itself: otherwise a plugin that
// receives a token can replay it against any other plugin.
package pluginui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	Audience = "flynn-plugin-ui"
	Issuer   = "flynn-dashboard"

	// JWKSTimeout bounds one JWKS fetch.
	JWKSTimeout = 3 * time.Second
	// JWKSTTL is how long fetched keys are trusted before a refetch.
	JWKSTTL = 5 * time.Minute
	// jwksMinRefetch limits refetches triggered by an unknown kid, so a
	// stream of bad tokens cannot turn into a stream of JWKS requests.
	jwksMinRefetch = 30 * time.Second
	clockLeeway    = 30 * time.Second
)

// Claims is the verified payload of a dashboard SSO token.
type Claims struct {
	Iss         string   `json:"iss"`
	Aud         string   `json:"aud"`
	Sub         string   `json:"sub"`
	Exp         int64    `json:"exp"`
	Iat         int64    `json:"iat"`
	Email       string   `json:"email"`
	AppID       string   `json:"app_id"`
	AppName     string   `json:"app_name"`
	Plugin      string   `json:"plugin"`
	Permissions []string `json:"permissions"`
	ResourceID  string   `json:"resource_id"`
	// AppGrants is set on cluster-level tokens: the user's permissions per app.
	AppGrants []AppGrant `json:"app_grants"`
	// ControllerToken is the user's controller access token. Never log it.
	ControllerToken string `json:"controller_token"`
}

// AppGrant is one app's permissions on a cluster-nav SSO token.
type AppGrant struct {
	AppID       string   `json:"app_id"`
	Permissions []string `json:"permissions"`
}

// Verifier checks SSO tokens against the dashboard JWKS for one plugin.
type Verifier struct {
	// JWKSURL is the dashboard JWKS endpoint (DASHBOARD_SSO_JWKS_URL).
	JWKSURL string
	// Plugin is the install name this process serves ("kafka"). Tokens minted
	// for any other plugin are refused. An empty name refuses everything.
	Plugin string
	// Client fetches the JWKS. Nil uses a client with JWKSTimeout.
	Client *http.Client
	// Now is the clock; nil uses time.Now.
	Now func() time.Time

	mu      sync.Mutex
	keys    map[string]*ecdsa.PublicKey
	fetched time.Time
	tried   time.Time
}

// NewVerifier returns a Verifier for plugin using the given JWKS URL.
func NewVerifier(jwksURL, plugin string) *Verifier {
	return &Verifier{JWKSURL: strings.TrimSpace(jwksURL), Plugin: strings.TrimSpace(plugin)}
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) client() *http.Client {
	if v.Client != nil {
		return v.Client
	}
	return defaultClient
}

var defaultClient = &http.Client{Timeout: JWKSTimeout}

// Verify checks the token signature and claims and returns the claims.
func (v *Verifier) Verify(raw string) (*Claims, error) {
	if v.Plugin == "" {
		return nil, errors.New("SSO plugin name is not configured")
	}
	if v.JWKSURL == "" {
		return nil, errors.New("SSO JWKS is not configured")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid SSO JWT")
	}
	hdrRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("invalid SSO JWT header")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrRaw, &hdr); err != nil {
		return nil, errors.New("invalid SSO JWT header")
	}
	if hdr.Alg != "ES256" {
		return nil, errors.New("unsupported SSO algorithm")
	}
	if hdr.Kid == "" {
		return nil, errors.New("SSO token missing kid")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("invalid SSO signature")
	}
	key, err := v.key(hdr.Kid)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(key, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, errors.New("SSO signature mismatch")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid SSO JWT payload")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("invalid SSO JWT claims")
	}
	if c.Iss != Issuer {
		return nil, errors.New("unexpected SSO issuer")
	}
	if c.Aud != Audience {
		return nil, errors.New("unexpected SSO audience")
	}
	if c.Exp <= 0 {
		return nil, errors.New("SSO token has no expiry")
	}
	if v.now().After(time.Unix(c.Exp, 0).Add(clockLeeway)) {
		return nil, errors.New("SSO token expired")
	}
	if c.Plugin != v.Plugin {
		return nil, errors.New("SSO token was not issued for this plugin")
	}
	if strings.TrimSpace(c.Sub) == "" {
		return nil, errors.New("SSO token has no subject")
	}
	return &c, nil
}

func (v *Verifier) key(kid string) (*ecdsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	stale := v.keys == nil || now.Sub(v.fetched) >= JWKSTTL
	if k, ok := v.keys[kid]; ok && !stale {
		return k, nil
	}
	// Refetch when the cache is stale, or when the kid is unknown (key
	// rotation), but not more often than jwksMinRefetch.
	if stale || now.Sub(v.tried) >= jwksMinRefetch {
		v.tried = now
		keys, err := v.fetch()
		if err == nil {
			v.keys, v.fetched = keys, now
		} else if v.keys == nil {
			return nil, err
		}
		// On error with cached keys, keep verifying against them.
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("SSO signing key not found")
}

func (v *Verifier) fetch() (map[string]*ecdsa.PublicKey, error) {
	req, err := http.NewRequest(http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := v.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: status %d", res.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			Kid string `json:"kid"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errors.New("jwks: invalid document")
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" || k.Kid == "" {
			continue
		}
		xb, err1 := base64.RawURLEncoding.DecodeString(k.X)
		yb, err2 := base64.RawURLEncoding.DecodeString(k.Y)
		if err1 != nil || err2 != nil {
			continue
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(xb), Y: new(big.Int).SetBytes(yb)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("jwks: no usable keys")
	}
	return out, nil
}
