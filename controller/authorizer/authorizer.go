package authorizer

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/cryptobyte/asn1"
	"google.golang.org/protobuf/proto"
)

var ErrInvalid = errors.New("invalid authorization")

type Authorizer struct {
	authKeys []string
	authIDs  []string

	tokenKey         *ecdsa.PublicKey
	tokenMaxValidity time.Duration
}

type AppGrant struct {
	AppID       string
	Permissions []string
}

type Token struct {
	ID         string
	User       string
	UserID     string
	ClusterKey bool
	// Blocked is set when the user or their personal account is suspended or
	// disabled. The HTTP middleware denies the request. A nil token is never
	// blocked; HasClusterAdmin still treats nil as legacy admin.
	Blocked   bool
	Scopes    []string
	AppGrants []AppGrant
	// IssuedAt is a signed login token's issue time. It is zero for the
	// cluster key and personal access tokens.
	IssuedAt time.Time
}

// HasClusterAdmin reports full cluster access: the cluster install key, or an
// explicit cluster:admin / * scope. A nil token is treated as admin only for
// legacy internal callers; HTTP middleware never authorizes a nil token
// (AuthorizeRequest returns ErrInvalid instead). An empty Token with no
// ClusterKey, scopes, or grants is not admin.
func (t *Token) HasClusterAdmin() bool {
	if t == nil {
		return true
	}
	if t.ClusterKey {
		return true
	}
	for _, s := range t.Scopes {
		if s == "cluster:admin" || s == "*" {
			return true
		}
	}
	return false
}

// BearerScopedToApps is true for JWTs that are not cluster-wide.
func (t *Token) BearerScopedToApps() bool {
	if t == nil || t.ClusterKey {
		return false
	}
	return !t.HasClusterAdmin() && len(t.AppGrants) > 0
}

func ParseTokenKey(tk string) (*ecdsa.PublicKey, error) {
	if tk == "" {
		return nil, nil
	}
	var tokenKey *ecdsa.PublicKey
	tokenKeyBytes, err := base64.URLEncoding.DecodeString(tk)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKIXPublicKey(tokenKeyBytes)
	if err != nil {
		return nil, err
	}
	var ok bool
	tokenKey, ok = k.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("unexpected token key type %T, want *ecdsa.PublicKey", k)
	}
	return tokenKey, nil
}

// ServiceTokenMaxValidity is the ceiling for account:quota and acme:issue.
// Every other scope, including a mix with cluster:admin, keeps tokenMaxValidity.
const ServiceTokenMaxValidity = 180 * 24 * time.Hour

// ServiceScopeLifetime reports the longer ceiling when every scope is a
// service scope. An empty list or any other scope returns false.
func ServiceScopeLifetime(scopes []string) (time.Duration, bool) {
	if len(scopes) == 0 {
		return 0, false
	}
	for _, s := range scopes {
		switch s {
		case "account:quota", "acme:issue":
		default:
			return 0, false
		}
	}
	return ServiceTokenMaxValidity, true
}

func ParseTokenMaxValidity(tv string) (time.Duration, error) {
	if tv == "" {
		return time.Hour, nil
	}
	ti, err := strconv.Atoi(tv)
	if err != nil {
		return 0, err
	}
	return time.Duration(ti) * time.Second, nil
}

func New(authKeys, authIDs []string, tokenKey *ecdsa.PublicKey, tokenMaxValidity time.Duration) *Authorizer {
	return &Authorizer{
		authKeys:         authKeys,
		authIDs:          authIDs,
		tokenKey:         tokenKey,
		tokenMaxValidity: tokenMaxValidity,
	}
}

func (a *Authorizer) AuthorizeKey(key string) (*Token, error) {
	if key == "" {
		return nil, ErrInvalid
	}
	for i, k := range a.authKeys {
		if len(key) == len(k) && subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
			token := &Token{ClusterKey: true}
			if len(a.authIDs) == len(a.authKeys) {
				token.ID = a.authIDs[i]
			}
			return token, nil
		}
	}
	return nil, ErrInvalid
}

func (a *Authorizer) AuthorizeRequest(req *http.Request) (*Token, error) {
	if auth := req.Header.Get("Authorization"); auth != "" && strings.HasPrefix(auth, "Bearer ") {
		return a.AuthorizeToken(auth)
	}
	user, password, _ := req.BasicAuth()
	if user == "Bearer" {
		return a.AuthorizeToken(password)
	}
	return a.AuthorizeKey(password)
}

func (a *Authorizer) AuthorizeToken(token string) (*Token, error) {
	if a.tokenKey == nil {
		return nil, ErrInvalid
	}

	token = strings.TrimPrefix(token, "Bearer ")
	if splitToken := strings.SplitN(token, ".", 2); len(splitToken) > 1 {
		token = splitToken[1]
	}
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding")
	}

	t := &api.AccessToken{}
	if err := protoVerifyUnmarshal(a.tokenKey, b, t); err != nil {
		return nil, err
	}

	if t.IssueTime == nil || t.ExpireTime == nil {
		return nil, fmt.Errorf("invalid token timestamp")
	}
	iss := t.IssueTime.AsTime()
	exp := t.ExpireTime.AsTime()
	if iss.IsZero() || exp.IsZero() {
		return nil, fmt.Errorf("invalid token timestamp")
	}
	max := a.tokenMaxValidity
	if extra, ok := ServiceScopeLifetime(t.GetScopes()); ok && extra > max {
		max = extra
	}
	if exp.Sub(iss) > max {
		return nil, fmt.Errorf("invalid token validity period")
	}
	if time.Now().After(exp) {
		return nil, fmt.Errorf("expired token")
	}

	idBytes := sha256.Sum256(b)
	grants := make([]AppGrant, 0, len(t.GetAppGrants()))
	for _, g := range t.GetAppGrants() {
		perms := append([]string(nil), g.GetPermissions()...)
		grants = append(grants, AppGrant{AppID: g.GetAppId(), Permissions: perms})
	}
	scopes := append([]string(nil), t.GetScopes()...)

	return &Token{
		ID:         strings.TrimRight(base64.URLEncoding.EncodeToString(idBytes[:]), "="),
		User:       t.UserEmail,
		UserID:     t.GetUserId(),
		ClusterKey: false,
		Scopes:     scopes,
		AppGrants:  grants,
		IssuedAt:   iss,
	}, nil
}

func protoVerifyUnmarshal(k *ecdsa.PublicKey, b []byte, m proto.Message) error {
	signed := &api.SignedData{}
	if err := proto.Unmarshal(b, signed); err != nil {
		return fmt.Errorf("invalid signed token")
	}

	h := sha256.New()
	h.Write(signed.Data)

	if !verifyASN1(k, h.Sum(nil), signed.Signature) {
		return fmt.Errorf("incorrect signature")
	}

	if err := proto.Unmarshal(signed.Data, m); err != nil {
		return fmt.Errorf("invalid token")
	}
	return nil
}

// This should be replaced with ecdsa.VerifyASN1 when Go 1.15 is available
// https://go.googlesource.com/go/+/8c09e8af3633b0c08d2c309e56a58124dfee3d7c
func verifyASN1(pub *ecdsa.PublicKey, hash, sig []byte) bool {
	var (
		r, s  = &big.Int{}, &big.Int{}
		inner cryptobyte.String
	)
	input := cryptobyte.String(sig)
	if !input.ReadASN1(&inner, asn1.SEQUENCE) ||
		!input.Empty() ||
		!inner.ReadASN1Integer(r) ||
		!inner.ReadASN1Integer(s) ||
		!inner.Empty() {
		return false
	}
	return ecdsa.Verify(pub, hash, r, s)
}
