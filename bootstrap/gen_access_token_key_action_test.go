package bootstrap

import (
	"testing"
	"time"

	api "github.com/flynn/flynn/controller/api"
	"github.com/flynn/flynn/controller/authorizer"
	"github.com/flynn/flynn/controller/tokensigner"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestGenAccessTokenKey verifies that the generated keypair round-trips: the
// private half signs a token that the public half verifies, matching how
// gitreceive (signing key) and the controller/tarreceive (verify key) use the
// two encodings emitted into step data.
func TestGenAccessTokenKey(t *testing.T) {
	s := &State{StepData: make(map[string]interface{})}
	a := &GenAccessTokenKeyAction{ID: "access-token-key"}
	if err := a.Run(s); err != nil {
		t.Fatalf("run: %s", err)
	}

	data, ok := s.StepData["access-token-key"].(*AccessTokenKeyData)
	if !ok {
		t.Fatalf("step data type = %T, want *AccessTokenKeyData", s.StepData["access-token-key"])
	}
	if data.PublicKey == "" || data.PrivateKey == "" {
		t.Fatal("expected both public and private keys to be set")
	}
	if data.PublicKey == data.PrivateKey {
		t.Fatal("public and private keys must differ")
	}

	signer, err := tokensigner.ParseSigningKey(data.PrivateKey)
	if err != nil {
		t.Fatalf("parse signing key: %s", err)
	}
	if signer == nil {
		t.Fatal("expected non-nil signer")
	}
	pk, err := authorizer.ParseTokenKey(data.PublicKey)
	if err != nil {
		t.Fatalf("parse token key: %s", err)
	}

	now := time.Now()
	tokenStr, err := signer.Sign(&api.AccessToken{
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{"build:artifacts"},
		AppGrants: []*api.AppGrant{
			{AppId: "app-1", Permissions: []string{"app:write"}},
		},
	})
	if err != nil {
		t.Fatalf("sign: %s", err)
	}
	if _, err := authorizer.New(nil, nil, pk, time.Hour).AuthorizeToken(tokenStr); err != nil {
		t.Fatalf("verify token signed with generated key: %s", err)
	}
}
