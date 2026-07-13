package authz

import (
	"net/http"
	"testing"

	"github.com/flynn/flynn/controller/authorizer"
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
		{"app_write_can_post_deploy_route", appWrite, http.MethodPost, "/apps/app-1/deploy", true},
		{"wrong_app_denied", wrongApp, http.MethodGet, "/apps/app-1", false},

		{"deploy_grant_allows_named_deploy_route", appDeploy, http.MethodPost, "/apps/app-1/deploy", true},
		// app:deploy satisfies rkAppWrite (see grantCovers), not only POST …/deploy.
		{"deploy_grant_allows_post_subresource", appDeploy, http.MethodPost, "/apps/app-1/releases", true},

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
