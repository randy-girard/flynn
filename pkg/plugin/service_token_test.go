package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	api "github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/servicetoken"
	"github.com/randy-girard/flynn/controller/tokensigner"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestApplyServiceTokenStripsClusterKey(t *testing.T) {
	pub, priv, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CONTROLLER_KEY": "cluster-god-key", "AUTH_KEY": "cluster-god-key"}
	cluster := map[string]string{"ACCESS_TOKEN_SIGNING_KEY": priv}
	spec := &ServiceToken{
		Env:         "QUOTA_TOKEN",
		Scope:       "account:quota",
		Caller:      "billing",
		Strip:       []string{"CONTROLLER_KEY", "AUTH_KEY", "ACCESS_TOKEN_SIGNING_KEY", "ACCESS_TOKEN_PRIVATE_KEY"},
		AuthHashEnv: "BILLING_AUTH_HASH",
	}
	if err := ApplyServiceToken(env, nil, cluster, spec); err != nil {
		t.Fatal(err)
	}
	for _, k := range spec.Strip {
		if _, ok := env[k]; ok {
			t.Fatalf("release env still has %s", k)
		}
	}
	if env["QUOTA_TOKEN"] == "" || strings.Contains(env["QUOTA_TOKEN"], "cluster-god-key") {
		t.Fatal("quota token missing or contains the cluster key")
	}
	if env["BILLING_AUTH_HASH"] == "" {
		t.Fatal("auth hash was not recorded")
	}
	key, err := authorizer.ParseTokenKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := authorizer.New(nil, nil, key, 0).AuthorizeToken(env["QUOTA_TOKEN"])
	if err != nil {
		t.Fatal(err)
	}
	if tok.ClusterKey || tok.HasClusterAdmin() || len(tok.Scopes) != 1 || tok.Scopes[0] != "account:quota" {
		t.Fatalf("token = %+v", tok)
	}
	kept := map[string]string{"QUOTA_TOKEN": env["QUOTA_TOKEN"], "CONTROLLER_KEY": "cluster-god-key"}
	next := map[string]string{"CONTROLLER_KEY": "cluster-god-key"}
	if err := ApplyServiceToken(next, kept, nil, spec); err != nil {
		t.Fatal(err)
	}
	if next["QUOTA_TOKEN"] != kept["QUOTA_TOKEN"] {
		t.Fatal("an existing service token must be preserved across update")
	}
	if _, ok := next["CONTROLLER_KEY"]; ok {
		t.Fatal("update must still strip CONTROLLER_KEY")
	}
}

func TestApplyServiceTokenFailsWithoutSigner(t *testing.T) {
	env := map[string]string{"CONTROLLER_KEY": "cluster-god-key"}
	err := ApplyServiceToken(env, nil, map[string]string{}, &ServiceToken{
		Env: "QUOTA_TOKEN", Scope: "account:quota", Strip: []string{"CONTROLLER_KEY"},
	})
	if err == nil {
		t.Fatal("install must fail when the token cannot be minted")
	}
	if _, ok := env["CONTROLLER_KEY"]; !ok {
		t.Fatal("a failed mint must not be the path that strips the key before the error is returned")
	}
}

// signedServiceToken signs a service token with explicit issue and expiry
// times so a test can build an expired or nearly expired one.
func signedServiceToken(t *testing.T, priv, scope string, issued, expires time.Time) string {
	t.Helper()
	signer, err := tokensigner.ParseSigningKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := signer.Sign(&api.AccessToken{
		UserId:     "billing",
		UserEmail:  "billing@flynn.local",
		IssueTime:  timestamppb.New(issued),
		ExpireTime: timestamppb.New(expires),
		Scopes:     []string{scope},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func tokenExpiry(t *testing.T, tok string) time.Time {
	t.Helper()
	at, err := servicetoken.Inspect(tok)
	if err != nil {
		t.Fatalf("minted token is not parseable: %v", err)
	}
	return at.ExpireTime.AsTime()
}

func TestApplyServiceTokenRemintsExpiredToken(t *testing.T) {
	_, priv, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	cluster := map[string]string{"ACCESS_TOKEN_SIGNING_KEY": priv}
	spec := &ServiceToken{Env: "QUOTA_TOKEN", Scope: "account:quota", Caller: "billing"}
	now := time.Now()
	fresh := signedServiceToken(t, priv, "account:quota", now.Add(-time.Hour), now.Add(100*24*time.Hour))
	cases := map[string]string{
		"expired":     signedServiceToken(t, priv, "account:quota", now.Add(-170*24*time.Hour), now.Add(-time.Hour)),
		"near expiry": signedServiceToken(t, priv, "account:quota", now.Add(-170*24*time.Hour), now.Add(5*24*time.Hour)),
		"wrong scope": signedServiceToken(t, priv, "acme:issue", now.Add(-time.Hour), now.Add(100*24*time.Hour)),
		"garbage":     "not-a-token",
	}
	for name, old := range cases {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"QUOTA_TOKEN": old}
			prev := map[string]string{"QUOTA_TOKEN": old}
			if err := ApplyServiceToken(env, prev, cluster, spec); err != nil {
				t.Fatal(err)
			}
			if env["QUOTA_TOKEN"] == old {
				t.Fatal("token was reused")
			}
			if exp := tokenExpiry(t, env["QUOTA_TOKEN"]); exp.Before(now.Add(100 * 24 * time.Hour)) {
				t.Fatalf("new token expires too soon: %v", exp)
			}
		})
	}
	t.Run("healthy token is kept", func(t *testing.T) {
		env := map[string]string{"QUOTA_TOKEN": fresh}
		if err := ApplyServiceToken(env, map[string]string{"QUOTA_TOKEN": fresh}, cluster, spec); err != nil {
			t.Fatal(err)
		}
		if env["QUOTA_TOKEN"] != fresh {
			t.Fatal("a healthy token must be preserved")
		}
	})
	t.Run("expired token without signer fails", func(t *testing.T) {
		old := cases["expired"]
		env := map[string]string{"QUOTA_TOKEN": old}
		if err := ApplyServiceToken(env, map[string]string{"QUOTA_TOKEN": old}, nil, spec); err == nil {
			t.Fatal("an expired token that cannot be re-minted must fail the apply")
		}
	})
}

func TestApplyServiceTokenRecomputesAuthHash(t *testing.T) {
	_, priv, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	spec := &ServiceToken{Env: "QUOTA_TOKEN", Scope: "account:quota", Caller: "billing", AuthHashEnv: "BILLING_AUTH_HASH", Strip: []string{"CONTROLLER_KEY"}}
	cluster := map[string]string{"ACCESS_TOKEN_SIGNING_KEY": priv}
	env := map[string]string{"CONTROLLER_KEY": "rotated-key", "BILLING_AUTH_HASH": "stale-hash"}
	if err := ApplyServiceToken(env, nil, cluster, spec); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("rotated-key"))
	if want := hex.EncodeToString(sum[:]); env["BILLING_AUTH_HASH"] != want {
		t.Fatalf("hash = %q, want %q", env["BILLING_AUTH_HASH"], want)
	}
	// No key available: keep what the previous release had.
	env = map[string]string{"BILLING_AUTH_HASH": "kept"}
	if err := ApplyServiceToken(env, map[string]string{"QUOTA_TOKEN": signedServiceToken(t, priv, "account:quota", time.Now(), time.Now().Add(150*24*time.Hour))}, cluster, spec); err != nil {
		t.Fatal(err)
	}
	if env["BILLING_AUTH_HASH"] != "kept" {
		t.Fatalf("hash = %q, want it kept when no cluster key is present", env["BILLING_AUTH_HASH"])
	}
}
