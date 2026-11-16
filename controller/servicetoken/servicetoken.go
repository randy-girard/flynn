// Package servicetoken mints long-lived credentials for billing quota writes
// and ACME certificate issuance. The scopes are not cluster admin.
package servicetoken

import (
	"fmt"
	"strings"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/tokensigner"
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
