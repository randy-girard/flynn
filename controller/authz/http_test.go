package authz

import (
	"net/http"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestHTTPAllowed(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	legacyFull := &authorizer.Token{}
	adminBearer := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	appRead := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:read"}}}}
	appWrite := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}}
	appDeploy := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:deploy"}}}}
	wrongApp := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "other", Permissions: []string{"app:write"}}}}

	// Build token: build:artifacts scope + an app grant for the app being built.
	buildTok := &authorizer.Token{
		Scopes:    []string{ScopeBuildArtifacts},
		AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}},
	}
	// build:artifacts scope with no app grant is worthless on its own.
	buildNoGrant := &authorizer.Token{Scopes: []string{ScopeBuildArtifacts}}

	cases := []struct {
		name    string
		tok     *authorizer.Token
		method  string
		path    string
		allowed bool
	}{
		{"nil_denied", nil, http.MethodGet, "/apps/app-1", false},

		{"cluster_key_any_route", clusterKey, http.MethodGet, "/providers", true},
		{"legacy_full_cluster_apps_list", legacyFull, http.MethodGet, "/apps", true},
		{"scoped_admin_providers", adminBearer, http.MethodGet, "/providers", true},

		{"app_read_can_get_app", appRead, http.MethodGet, "/apps/app-1", true},
		{"app_read_head_app", appRead, http.MethodHead, "/apps/app-1", true},
		{"app_read_cannot_post_subresource", appRead, http.MethodPost, "/apps/app-1/releases", false},
		{"app_read_cannot_list_apps", appRead, http.MethodGet, "/apps", false},

		{"app_write_can_post_release", appWrite, http.MethodPost, "/apps/app-1/releases", true},
		{"app_write_can_post_cluster_release", appWrite, http.MethodPost, "/releases", true},
		{"app_read_cannot_post_cluster_release", appRead, http.MethodPost, "/releases", false},
		{"app_admin_can_post_cluster_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodPost, "/releases", true},
		{"app_admin_can_post_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodPost, "/apps/app-1/releases", true},
		{"app_admin_can_deploy", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodPost, "/apps/app-1/deploy", true},
		{"app_write_can_post_deploy_route", appWrite, http.MethodPost, "/apps/app-1/deploy", true},
		{"wrong_app_denied", wrongApp, http.MethodGet, "/apps/app-1", false},

		{"deploy_grant_allows_named_deploy_route", appDeploy, http.MethodPost, "/apps/app-1/deploy", true},
		{"deploy_grant_cannot_post_subresource", appDeploy, http.MethodPost, "/apps/app-1/releases", false},
		{"deploy_grant_cannot_post_cluster_release", appDeploy, http.MethodPost, "/releases", false},
		{"deploy_grant_can_get_app", appDeploy, http.MethodGet, "/apps/app-1", true},

		// build:artifacts scope + app grant may create artifacts...
		{"build_token_can_post_artifacts", buildTok, http.MethodPost, "/artifacts", true},
		// ...but nothing else cluster-level, and only writes to its own app.
		{"build_token_cannot_get_artifacts", buildTok, http.MethodGet, "/artifacts", false},
		{"build_token_cannot_list_apps", buildTok, http.MethodGet, "/apps", false},
		{"build_token_can_write_its_app", buildTok, http.MethodPost, "/apps/app-1/releases", true},
		{"build_token_cannot_write_other_app", buildTok, http.MethodPost, "/apps/other/releases", false},
		// scope without an app grant grants nothing.
		{"build_scope_no_grant_denied", buildNoGrant, http.MethodPost, "/artifacts", false},
		// a plain app-write token cannot create artifacts.
		{"app_write_cannot_post_artifacts", appWrite, http.MethodPost, "/artifacts", false},

		{"app_write_can_psql_own_app", appWrite, http.MethodPost, "/apps/app-1/jobs", true},

		// Platform DBs: even an explicit grant on the app name is not enough.
		{"app_write_cannot_psql_controller", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "controller", Permissions: []string{"app:write"}}}}, http.MethodPost, "/apps/controller/jobs", false},
		{"app_read_cannot_get_controller_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "controller", Permissions: []string{"app:read"}}}}, http.MethodGet, "/apps/controller/release", false},
		{"app_write_cannot_psql_blobstore", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "blobstore", Permissions: []string{"app:write"}}}}, http.MethodPost, "/apps/blobstore/jobs", false},
		{"app_write_cannot_psql_router", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "router", Permissions: []string{"app:write"}}}}, http.MethodPost, "/apps/router/jobs", false},
		{"app_write_cannot_psql_postgres", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "postgres", Permissions: []string{"app:write"}}}}, http.MethodPost, "/apps/postgres/jobs", false},
		{"cluster_key_can_psql_controller", clusterKey, http.MethodPost, "/apps/controller/jobs", true},
		{"admin_scope_can_psql_blobstore", adminBearer, http.MethodPost, "/apps/blobstore/jobs", true},

		{"app_read_can_list_runtime_profiles", appRead, http.MethodGet, "/runtime-profiles", true},
		{"app_read_cannot_create_runtime_profile", appRead, http.MethodPost, "/runtime-profiles", false},
		{"app_write_cannot_put_runtime_settings", appWrite, http.MethodPut, "/cluster/runtime-settings", false},
		{"app_read_can_get_runtime_settings", appRead, http.MethodGet, "/cluster/runtime-settings", true},
		{"cluster_key_can_create_runtime_profile", clusterKey, http.MethodPost, "/runtime-profiles", true},

		{"app_read_can_get_github_app", appRead, http.MethodGet, "/github/app", true},
		{"app_write_cannot_put_github_app", appWrite, http.MethodPut, "/github/app", false},
		{"admin_can_put_github_app", adminBearer, http.MethodPut, "/github/app", true},
		{"app_read_can_list_github_installations", appRead, http.MethodGet, "/github/installations", true},
		{"app_write_can_put_app_github", appWrite, http.MethodPut, "/apps/app-1/github", true},
		{"app_read_cannot_put_app_github", appRead, http.MethodPut, "/apps/app-1/github", false},
		{"deploy_grant_can_github_deploy", appDeploy, http.MethodPost, "/apps/app-1/github/deploy", true},
		{"app_read_cannot_github_deploy", appRead, http.MethodPost, "/apps/app-1/github/deploy", false},

		{"logs_read_can_get_log", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodGet, "/apps/app-1/log", true},
		{"logs_read_cannot_get_metrics", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodGet, "/apps/app-1/jobs-stats", false},
		{"scale_write_can_put_formation", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:scale:write"}}}}, http.MethodPut, "/apps/app-1/formations/rel-1", true},
		{"scale_write_cannot_deploy", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:scale:write"}}}}, http.MethodPost, "/apps/app-1/deploy", false},
		{"jobs_run_can_post_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:run"}}}}, http.MethodPost, "/apps/app-1/jobs", true},
		{"jobs_run_cannot_stop_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:run"}}}}, http.MethodDelete, "/apps/app-1/jobs/job-1", false},
		{"jobs_stop_can_delete_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:stop"}}}}, http.MethodDelete, "/apps/app-1/jobs/job-1", true},
		{"routes_write_can_post_route", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:routes:write"}}}}, http.MethodPost, "/apps/app-1/routes", true},
		{"env_write_can_put_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:env:write"}}}}, http.MethodPut, "/apps/app-1/release", true},
		{"env_write_can_post_cluster_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:env:write"}}}}, http.MethodPost, "/releases", true},
		{"scale_write_cannot_post_cluster_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:scale:write"}}}}, http.MethodPost, "/releases", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HTTPAllowed(tc.tok, tc.method, tc.path)
			if got != tc.allowed {
				t.Fatalf("HTTPAllowed(tok, %q, %q) = %v, want %v", tc.method, tc.path, got, tc.allowed)
			}
		})
	}
}

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
		{"legacy_full_allowed", &authorizer.Token{}, true},
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
