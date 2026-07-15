package tokensigner

import (
	"time"

	api "github.com/flynn/flynn/controller/api"
	"github.com/flynn/flynn/controller/authz"
	"github.com/flynn/flynn/controller/authorizer"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// KeyPairValid reports whether privateKey is the signing half of publicKey by
// minting a test token and verifying it the same way the controller does.
func KeyPairValid(publicKey, privateKey string) bool {
	if publicKey == "" || privateKey == "" {
		return false
	}
	signer, err := ParseSigningKey(privateKey)
	if err != nil || signer == nil {
		return false
	}
	pk, err := authorizer.ParseTokenKey(publicKey)
	if err != nil || pk == nil {
		return false
	}
	now := time.Now()
	tok, err := signer.Sign(&api.AccessToken{
		UserEmail:  "build:keypair-check",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(time.Minute)),
		Scopes:     []string{authz.ScopeBuildArtifacts},
		AppGrants: []*api.AppGrant{
			{AppId: "keypair-check", Permissions: []string{"app:write"}},
		},
	})
	if err != nil {
		return false
	}
	_, err = authorizer.New(nil, nil, pk, time.Hour).AuthorizeToken(tok)
	return err == nil
}
