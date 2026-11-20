// Package plugincaller carries the identity of the user behind a controller
// request to a plugin that the controller proxies.
//
// The controller calls a plugin with the cluster controller key whoever the
// user is, so the key alone says nothing about the caller. Before it forwards a
// request the controller drops any client-supplied Header and sets a fresh one
// with Sign. The value is the caller (admin flag, user id, personal account and
// the app grants the controller resolved) plus the request method, URI and an
// expiry, authenticated with an HMAC keyed by the controller key. Only a holder
// of that key can mint one, and a value copied from one request does not verify
// on another route or after it expires.
//
// A plugin calls FromRequest with its CONTROLLER_KEY. No header means the
// controller did not say (an older controller, or a direct call with the key);
// an invalid header must be refused, never treated as no header.
package plugincaller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
)

const (
	// Header holds the signed caller identity.
	Header = "X-Flynn-Caller"
	// TTL is how long a signed identity is valid after the controller signs it.
	TTL = 2 * time.Minute
	// clockLeeway allows for clock skew between the controller and plugin hosts.
	clockLeeway = 30 * time.Second
	// maxHeaderLen bounds what FromRequest decodes.
	maxHeaderLen = 256 << 10
	version      = "v1"
	macContext   = "flynn-plugin-caller-v1\n"
)

var (
	// ErrNoCaller is returned by FromRequest when the request has no Header.
	ErrNoCaller = errors.New("no caller identity")
	// ErrInvalid is returned for a Header that does not verify.
	ErrInvalid = errors.New("invalid caller identity")
)

// AppGrant is one app's permissions, as the controller resolved them for the
// caller.
type AppGrant struct {
	AppID       string   `json:"app_id"`
	Permissions []string `json:"permissions"`
}

// Caller is the identity the controller vouches for.
type Caller struct {
	// Admin is true for a cluster admin (the controller key, or a cluster:admin
	// token).
	Admin bool `json:"admin"`
	// UserID is the controller user id, empty for the controller key and for
	// tokens that are not bound to a user.
	UserID string `json:"user_id,omitempty"`
	// Account is the caller's personal account (user:<id>), empty without a
	// user id.
	Account string `json:"account,omitempty"`
	// AppGrants are the caller's per-app permissions. Use Can and AppIDs; they
	// apply the controller's permission implications.
	AppGrants []AppGrant `json:"app_grants,omitempty"`
}

// Can reports whether the caller holds perm (for example "app:github:write")
// on the app with exactly this id. A cluster admin can do anything.
func (c *Caller) Can(appID, perm string) bool {
	if c == nil {
		return false
	}
	if c.Admin {
		return true
	}
	if appID == "" {
		return false
	}
	for _, g := range c.AppGrants {
		if g.AppID == appID && authz.HasAppPermission(g.Permissions, perm) {
			return true
		}
	}
	return false
}

// AppIDs returns the ids of the apps on which the caller holds perm. It is nil
// for an admin, who is not limited to a list.
func (c *Caller) AppIDs(perm string) []string {
	if c == nil || c.Admin {
		return nil
	}
	var ids []string
	for _, g := range c.AppGrants {
		if g.AppID != "" && authz.HasAppPermission(g.Permissions, perm) {
			ids = append(ids, g.AppID)
		}
	}
	return ids
}

// FromToken builds the Caller for a controller token after the controller
// authorized (and, for a user, expanded) it. A nil token is not vouched for
// and returns nil.
func FromToken(tok *authorizer.Token) *Caller {
	if tok == nil {
		return nil
	}
	c := &Caller{Admin: tok.HasClusterAdmin(), UserID: tok.UserID}
	if tok.UserID != "" {
		c.Account = "user:" + tok.UserID
	}
	if !c.Admin {
		for _, g := range tok.AppGrants {
			if g.AppID == "" || len(g.Permissions) == 0 {
				continue
			}
			c.AppGrants = append(c.AppGrants, AppGrant{AppID: g.AppID, Permissions: append([]string(nil), g.Permissions...)})
		}
	}
	return c
}

type envelope struct {
	Caller
	Method string `json:"method"`
	URI    string `json:"uri"`
	Exp    int64  `json:"exp"`
}

// Sign returns the Header value for c on a method request to uri (path and
// query, as http.Request.URL.RequestURI returns it). key is the controller key
// the plugin will verify with.
func Sign(key string, c Caller, method, uri string, now time.Time) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("plugincaller: no controller key")
	}
	raw, err := json.Marshal(envelope{Caller: c, Method: method, URI: uri, Exp: now.Add(TTL).Unix()})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return version + "." + payload + "." + base64.RawURLEncoding.EncodeToString(mac(key, payload)), nil
}

// SetHeader drops any Header already on h (a client's) and, when value is not
// empty, sets it.
func SetHeader(h http.Header, value string) {
	h.Del(Header)
	if value != "" {
		h.Set(Header, value)
	}
}

// FromRequest verifies the Header on r with the controller key. It returns
// ErrNoCaller when the header is absent and ErrInvalid when it does not verify
// for this request.
func FromRequest(r *http.Request, key string) (*Caller, error) {
	return verify(r, key, time.Now())
}

func verify(r *http.Request, key string, now time.Time) (*Caller, error) {
	vals := r.Header.Values(Header)
	if len(vals) == 0 {
		return nil, ErrNoCaller
	}
	if len(vals) != 1 || strings.TrimSpace(key) == "" || len(vals[0]) > maxHeaderLen {
		return nil, ErrInvalid
	}
	parts := strings.Split(strings.TrimSpace(vals[0]), ".")
	if len(parts) != 3 || parts[0] != version {
		return nil, ErrInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac(key, parts[1])) {
		return nil, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalid
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, ErrInvalid
	}
	if env.Exp <= 0 || now.After(time.Unix(env.Exp, 0).Add(clockLeeway)) {
		return nil, ErrInvalid
	}
	if env.Method != r.Method || env.URI != r.URL.RequestURI() {
		return nil, ErrInvalid
	}
	c := env.Caller
	return &c, nil
}

func mac(key, payload string) []byte {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(macContext))
	m.Write([]byte(payload))
	return m.Sum(nil)
}
