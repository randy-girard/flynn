// Package servicetoken mints long-lived credentials for billing quota writes
// and ACME certificate issuance. The scopes are not cluster admin.
package servicetoken

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/tokensigner"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DefaultTTL is the lifetime install and token:rotate use.
const DefaultTTL = authorizer.ServiceTokenMaxValidity

// Mint signs a service token. scope must be account:quota or acme:issue.
// The token has no app grants and does not set the cluster-key flag.
func Mint(signer *tokensigner.Signer, scope, caller string, ttl time.Duration) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("ACCESS_TOKEN_SIGNING_KEY is required to mint a service token")
	}
	scope = strings.TrimSpace(scope)
	if _, ok := authorizer.ServiceScopeLifetime([]string{scope}); !ok {
		return "", fmt.Errorf("unsupported service scope %q", scope)
	}
	caller = strings.TrimSpace(caller)
	if caller == "" {
		caller = scope
	}
	if ttl <= 0 || ttl > DefaultTTL {
		ttl = DefaultTTL
	}
	now := time.Now()
	return signer.Sign(&api.AccessToken{
		UserId:     caller,
		UserEmail:  caller + "@flynn.local",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(ttl)),
		Scopes:     []string{scope},
	})
}

// Inspect decodes a service token WITHOUT verifying its signature. It is for
// deciding whether to re-mint a token the cluster already issued (expiry,
// scope); it must never be used to authorize anything.
func Inspect(token string) (*api.AccessToken, error) {
	token = strings.TrimPrefix(strings.TrimSpace(token), "Bearer ")
	if parts := strings.SplitN(token, ".", 2); len(parts) > 1 {
		token = parts[1]
	}
	b, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding")
	}
	signed := &api.SignedData{}
	if err := proto.Unmarshal(b, signed); err != nil {
		return nil, fmt.Errorf("invalid signed token")
	}
	t := &api.AccessToken{}
	if err := proto.Unmarshal(signed.Data, t); err != nil {
		return nil, fmt.Errorf("invalid token")
	}
	if t.IssueTime == nil || t.ExpireTime == nil {
		return nil, fmt.Errorf("invalid token timestamp")
	}
	return t, nil
}

// RenewBefore is how long before expiry a service token is replaced.
const RenewBefore = 30 * 24 * time.Hour

// Reusable reports whether an existing token still carries exactly scope and
// is not within RenewBefore of expiry. The reason is empty when it is.
func Reusable(token, scope string, now time.Time) (bool, string) {
	t, err := Inspect(token)
	if err != nil {
		return false, "unparseable: " + err.Error()
	}
	if got := t.GetScopes(); len(got) != 1 || got[0] != strings.TrimSpace(scope) {
		return false, fmt.Sprintf("scope %v, want %q", got, scope)
	}
	exp := t.ExpireTime.AsTime()
	if !now.Before(exp) {
		return false, "expired " + exp.UTC().Format(time.RFC3339)
	}
	if exp.Sub(now) < RenewBefore {
		return false, "expires " + exp.UTC().Format(time.RFC3339)
	}
	return true, ""
}
