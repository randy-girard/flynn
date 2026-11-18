// Package buildtoken mints the short-lived credential a compile uses.
// The controller mints it when the job is about to start. gitreceive does not.
package buildtoken

import (
	"fmt"
	"strings"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// DefaultTTL is the build timeout and token lifetime when none is set.
const DefaultTTL = 15 * time.Minute

// MaxTTL is the cluster ceiling. Queue time does not consume it: the token
// is minted at start.
const MaxTTL = 30 * time.Minute

// RateLimitPrefix marks a token's UserId as a build rate-limit identity, not a
// user. A build credential is never the owner: the controller does not expand
// it into the owner's account and it cannot call user-session routes.
const RateLimitPrefix = "build:"

// RateLimitID is the identity a build credential carries in UserId, so every
// build for one owner shares one controller rate-limit bucket that is separate
// from the owner's own bucket. It is "" when the app has no owner.
func RateLimitID(app *ct.App) string {
	if id := ownerID(app); id != "" {
		return RateLimitPrefix + id
	}
	return ""
}

// ownerID is the app owner as the controller keys accounts. A user owner is
// stored without the user: prefix, matching dashboard JWTs. An org owner
// keeps org: so the id cannot collide with a user id.
func ownerID(app *ct.App) string {
	if app == nil {
		return ""
	}
	owner := strings.TrimSpace(app.OwnerAccount)
	switch {
	case strings.HasPrefix(owner, "user:"):
		if id := strings.TrimPrefix(owner, "user:"); id != "" {
			return id
		}
	case owner != "":
		return owner
	}
	return strings.TrimSpace(app.CreatedBy)
}

// ClampTTL bounds a requested lifetime to (0, MaxTTL]. Non-positive uses DefaultTTL.
func ClampTTL(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTTL
	}
	if d > MaxTTL {
		return MaxTTL
	}
	return d
}

// Mint signs an app-scoped token. A nil signer is an error. The token is not
// a cluster admin and cannot write to other apps.
func Mint(signer *tokensigner.Signer, app *ct.App, ttl time.Duration) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("ACCESS_TOKEN_SIGNING_KEY is required to mint a scoped build token")
	}
	if app == nil {
		return "", fmt.Errorf("app is required to mint a build token")
	}
	ttl = ClampTTL(ttl)
	now := time.Now()
	return signer.Sign(&api.AccessToken{
		UserId:     RateLimitID(app),
		UserEmail:  "build:" + app.Name,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(ttl)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants: []*api.AppGrant{
			{AppId: app.ID, Permissions: []string{"app:write"}},
		},
	})
}

// MintReceive signs the credential flynn-receiver uses for one app.
// It is not a cluster admin. The lifetime stays inside the normal token
// cap so a long-running billing or ACME exception is not required.
func MintReceive(signer *tokensigner.Signer, app *ct.App, ttl time.Duration) (string, error) {
	if signer == nil {
		return "", fmt.Errorf("ACCESS_TOKEN_SIGNING_KEY is required to mint a receive token")
	}
	if app == nil || strings.TrimSpace(app.ID) == "" {
		return "", fmt.Errorf("app is required to mint a receive token")
	}
	ttl = ClampTTL(ttl)
	now := time.Now()
	// events:read on the app's own id: DeployAppRelease and ScaleAppRelease
	// wait on that app's deployment and job events.
	grants := []*api.AppGrant{{AppId: app.ID, Permissions: []string{"app:write", authz.ScopeEventsRead}}}
	if name := strings.TrimSpace(app.Name); name != "" && name != app.ID {
		grants = append(grants, &api.AppGrant{AppId: name, Permissions: []string{"app:write"}})
	}
	return signer.Sign(&api.AccessToken{
		UserId:     RateLimitID(app),
		UserEmail:  "receive:" + app.Name,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(ttl)),
		Scopes:     []string{authz.ScopeGitreceiveBuild, authz.ScopeEventsRead},
		AppGrants:  grants,
	})
}
