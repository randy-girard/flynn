package authz

import (
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestCanCreateReleaseForApp(t *testing.T) {
	envTok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{PermAppEnvWrite}}}}
	scaleTok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{PermAppScaleWrite}}}}
	if !CanCreateReleaseForApp(envTok, "app-1") {
		t.Fatal("env:write may create a release for its app")
	}
	if CanCreateReleaseForApp(envTok, "other") {
		t.Fatal("env:write must not create a release for another app")
	}
	if CanCreateReleaseForApp(scaleTok, "app-1") {
		t.Fatal("scale:write must not create a release")
	}
	if CanCreateReleaseForApp(nil, "app-1") {
		t.Fatal("nil token must not create a release")
	}
	if !CanCreateReleaseForApp(&authorizer.Token{ClusterKey: true}, "app-1") {
		t.Fatal("cluster key may create a release")
	}
}

func TestTarreceiveAllowed(t *testing.T) {
	cases := []struct {
		name    string
		tok     *authorizer.Token
		allowed bool
	}{
		{"nil_denied", nil, false},
		{"cluster_key_allowed", &authorizer.Token{ClusterKey: true}, true},
		{"empty_token_denied", &authorizer.Token{}, false},
		{"admin_scope_allowed", &authorizer.Token{Scopes: []string{"cluster:admin"}}, true},
		{
			"build_token_allowed",
			&authorizer.Token{
				Scopes:    []string{ScopeBuildArtifacts},
				AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}},
			},
			true,
		},
		{"build_scope_no_grant_denied", &authorizer.Token{Scopes: []string{ScopeBuildArtifacts}}, false},
		{
			"plain_app_token_denied",
			&authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TarreceiveAllowed(tc.tok); got != tc.allowed {
				t.Fatalf("TarreceiveAllowed(%s) = %v, want %v", tc.name, got, tc.allowed)
			}
		})
	}
}

func TestSystemAppAllowed(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	appWrite := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "controller", Permissions: []string{"app:write"}}}}
	cases := []struct {
		name   string
		tok    *authorizer.Token
		system bool
		want   bool
	}{
		{"user_app_nil_tok", nil, false, true},
		{"user_app_app_token", appWrite, false, true},
		{"system_app_nil_tok", nil, true, false},
		{"system_app_app_grant", appWrite, true, false},
		{"system_app_cluster_key", clusterKey, true, true},
		{"system_app_cluster_admin_scope", &authorizer.Token{Scopes: []string{"cluster:admin"}}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SystemAppAllowed(tc.tok, tc.system); got != tc.want {
				t.Fatalf("SystemAppAllowed(%s, system=%v) = %v, want %v", tc.name, tc.system, got, tc.want)
			}
		})
	}
	if !IsPlatformAppName("controller") || !IsPlatformAppName("blobstore") {
		t.Fatal("controller and blobstore must be platform app names")
	}
	if !IsPlatformAppName("router") || !IsPlatformAppName("postgres") {
		t.Fatal("router and postgres must be platform app names")
	}
	if IsPlatformAppName("myapp") || IsPlatformAppName("redis-11111111-2222-3333-4444-555555555555") {
		t.Fatal("user apps and redis appliances must not match platform names")
	}
	if IsPlatformAppName("") || IsPlatformAppName("Controller") {
		t.Fatal("empty and mixed-case names must not match platform apps")
	}
}

type ctxValuer struct {
	key, val interface{}
}

func (c *ctxValuer) Value(key interface{}) interface{} {
	if c != nil && key == c.key {
		return c.val
	}
	return nil
}

func TestTokenFromContext(t *testing.T) {
	if TokenFromContext(nil) != nil {
		t.Fatal("nil context must yield a nil token")
	}
	if TokenFromContext(&ctxValuer{}) != nil {
		t.Fatal("missing key must yield a nil token")
	}
	tok := &authorizer.Token{ClusterKey: true}
	got := TokenFromContext(&ctxValuer{key: TokenContextKey, val: tok})
	if got != tok {
		t.Fatalf("TokenFromContext = %v, want the stored token", got)
	}
	if TokenFromContext(&ctxValuer{key: TokenContextKey, val: "nope"}) != nil {
		t.Fatal("wrong value type must yield a nil token")
	}
}

func TestHideInternalProcesses(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	adminJWT := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	appRead := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:read"}}}}
	cases := []struct {
		name   string
		tok    *authorizer.Token
		system bool
		want   bool
	}{
		{"cluster_key_user_app", clusterKey, false, false},
		{"nil_tok_user_app", nil, false, false},
		{"admin_jwt_user_app", adminJWT, false, true},
		{"app_grant_user_app", appRead, false, true},
		{"admin_jwt_system_app", adminJWT, true, false},
		{"cluster_key_system_app", clusterKey, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HideInternalProcesses(tc.tok, tc.system); got != tc.want {
				t.Fatalf("HideInternalProcesses(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestCanManageInternalProcessLimits(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	adminJWT := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	appWrite := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}}
	appAdmin := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}
	cases := []struct {
		name  string
		tok   *authorizer.Token
		appID string
		want  bool
	}{
		{"cluster_key", clusterKey, "app-1", true},
		{"admin_jwt", adminJWT, "app-1", false},
		{"nil_tok", nil, "app-1", true},
		{"app_write", appWrite, "app-1", false},
		{"app_admin", appAdmin, "app-1", false},
		{"app_admin_other_app", appAdmin, "other", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanManageInternalProcessLimits(tc.tok, tc.appID); got != tc.want {
				t.Fatalf("CanManageInternalProcessLimits(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestGitPushAllowed(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	adminJWT := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	legacyFull := &authorizer.Token{}
	appRead := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "uuid-1", Permissions: []string{PermAppRead}}}}
	appDeploy := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "uuid-1", Permissions: []string{PermAppDeploy}}}}
	appWrite := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "uuid-1", Permissions: []string{PermAppWrite}}}}
	appAdmin := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "uuid-1", Permissions: []string{PermAppAdmin}}}}
	buildTok := &authorizer.Token{
		Scopes:    []string{ScopeBuildArtifacts},
		AppGrants: []authorizer.AppGrant{{AppID: "uuid-1", Permissions: []string{PermAppWrite}}},
	}
	otherWrite := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "other", Permissions: []string{PermAppWrite}}}}
	grantByName := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "myapp", Permissions: []string{PermAppDeploy}}}}

	type check func(*authorizer.Token, string, string) bool
	cases := []struct {
		name    string
		fn      check
		tok     *authorizer.Token
		appID   string
		appName string
		want    bool
	}{
		{"nil_cannot_push", GitPushAllowed, nil, "uuid-1", "myapp", false},
		{"nil_cannot_fetch", GitFetchAllowed, nil, "uuid-1", "myapp", false},
		{"cluster_key_push_anywhere", GitPushAllowed, clusterKey, "uuid-1", "myapp", true},
		{"cluster_key_push_other", GitPushAllowed, clusterKey, "uuid-2", "other", true},
		{"admin_jwt_push_anywhere", GitPushAllowed, adminJWT, "uuid-2", "other", true},
		{"legacy_empty_cannot_push", GitPushAllowed, legacyFull, "uuid-1", "myapp", false},
		{"deploy_push_own_app", GitPushAllowed, appDeploy, "uuid-1", "myapp", true},
		{"write_push_own_app", GitPushAllowed, appWrite, "uuid-1", "myapp", true},
		{"admin_push_own_app", GitPushAllowed, appAdmin, "uuid-1", "myapp", true},
		{"build_token_push_own_app", GitPushAllowed, buildTok, "uuid-1", "myapp", true},
		{"build_token_cannot_push_other", GitPushAllowed, buildTok, "uuid-2", "other", false},
		{"read_cannot_push", GitPushAllowed, appRead, "uuid-1", "myapp", false},
		{"read_can_fetch_own_app", GitFetchAllowed, appRead, "uuid-1", "myapp", true},
		{"read_cannot_fetch_other", GitFetchAllowed, appRead, "uuid-2", "other", false},
		{"wrong_app_cannot_push", GitPushAllowed, otherWrite, "uuid-1", "myapp", false},
		{"grant_by_name_cannot_push", GitPushAllowed, grantByName, "uuid-1", "myapp", false},
		{"deploy_can_fetch_own", GitFetchAllowed, appDeploy, "uuid-1", "myapp", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.fn(tc.tok, tc.appID, tc.appName); got != tc.want {
				t.Fatalf("%s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
