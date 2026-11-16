package plugin

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/tokensigner"
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
