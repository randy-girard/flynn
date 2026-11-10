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

// UserID is the controller rate-limit account for a build. A user owner is
// stored without the user: prefix, matching dashboard JWTs. An org owner
// keeps org: so the bucket cannot collide with a user id.
func UserID(app *ct.App) string {
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
		UserId:     UserID(app),
		UserEmail:  "build:" + app.Name,
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(ttl)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants: []*api.AppGrant{
			{AppId: app.ID, Permissions: []string{"app:write"}},
		},
	})
}
