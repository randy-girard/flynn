package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
)

// These tests check route requirements through the real route table that
// registerRoutes builds. They moved here from controller/authz when the
// table replaced the prefix switch (CORE-15).

func httpAllowed(tok *authorizer.Token, method, path string) bool {
	return controllerRoutes.Allowed(tok, method, path, nil)
}

func httpAllowedResolved(tok *authorizer.Token, method, path string, resolve func(string) string) bool {
	return controllerRoutes.Allowed(tok, method, path, resolve)
}

func TestRouteAuthzCases(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	emptyTok := &authorizer.Token{}
	adminBearer := &authorizer.Token{Scopes: []string{"cluster:admin"}}
	appRead := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:read"}}}}
	appWrite := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}}
	appDeploy := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:deploy"}}}}
	wrongApp := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "other", Permissions: []string{"app:write"}}}}

	// Build token: build:artifacts scope + an app grant for the app being built.
	buildTok := &authorizer.Token{
		Scopes:    []string{authz.ScopeBuildArtifacts},
		AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}},
	}
	// build:artifacts scope with no app grant is worthless on its own.
	buildNoGrant := &authorizer.Token{Scopes: []string{authz.ScopeBuildArtifacts}}

	cases := []struct {
		name    string
		tok     *authorizer.Token
		method  string
		path    string
		allowed bool
	}{
		{"nil_denied", nil, http.MethodGet, "/apps/app-1", false},

		{"cluster_key_any_route", clusterKey, http.MethodGet, "/providers", true},
		{"empty_token_cannot_list_apps", emptyTok, http.MethodGet, "/apps", false},
		{"empty_token_cannot_get_app", emptyTok, http.MethodGet, "/apps/app-1", false},
		{"scoped_admin_providers", adminBearer, http.MethodGet, "/providers", true},

		{"app_read_can_get_app", appRead, http.MethodGet, "/apps/app-1", true},
		{"app_read_head_app", appRead, http.MethodHead, "/apps/app-1", true},
		{"app_read_cannot_delete_release", appRead, http.MethodDelete, "/apps/app-1/releases/rel-1", false},
		{"app_read_cannot_list_apps", appRead, http.MethodGet, "/apps", false},
		{"app_write_cannot_list_apps", appWrite, http.MethodGet, "/apps", false},
		{"app_admin_cannot_list_apps", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodGet, "/apps", false},
		{"app_read_cannot_head_apps", appRead, http.MethodHead, "/apps", false},

		{"app_write_can_delete_release", appWrite, http.MethodDelete, "/apps/app-1/releases/rel-1", true},
		{"app_write_can_post_cluster_release", appWrite, http.MethodPost, "/releases", true},
		{"app_read_cannot_post_cluster_release", appRead, http.MethodPost, "/releases", false},
		{"app_admin_can_post_cluster_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodPost, "/releases", true},
		{"app_admin_can_delete_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodDelete, "/apps/app-1/releases/rel-1", true},
		{"app_admin_can_deploy", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodPost, "/apps/app-1/deploy", true},
		{"app_write_can_post_deploy_route", appWrite, http.MethodPost, "/apps/app-1/deploy", true},
		{"wrong_app_denied", wrongApp, http.MethodGet, "/apps/app-1", false},

		{"deploy_grant_allows_named_deploy_route", appDeploy, http.MethodPost, "/apps/app-1/deploy", true},
		{"deploy_grant_cannot_delete_release", appDeploy, http.MethodDelete, "/apps/app-1/releases/rel-1", false},
		{"deploy_grant_cannot_post_cluster_release", appDeploy, http.MethodPost, "/releases", false},
		{"deploy_grant_can_get_app", appDeploy, http.MethodGet, "/apps/app-1", true},

		// build:artifacts scope + app grant may create artifacts...
		{"build_token_can_post_artifacts", buildTok, http.MethodPost, "/artifacts", true},
		// ...but nothing else cluster-level, and only writes to its own app.
		{"build_token_cannot_get_artifacts", buildTok, http.MethodGet, "/artifacts", false},
		{"build_token_cannot_list_apps", buildTok, http.MethodGet, "/apps", false},
		{"build_token_can_write_its_app", buildTok, http.MethodDelete, "/apps/app-1/releases/rel-1", true},
		{"build_token_cannot_write_other_app", buildTok, http.MethodDelete, "/apps/other/releases/rel-1", false},
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

		{"app_read_can_list_runtimes", appRead, http.MethodGet, "/runtimes", true},
		{"app_read_cannot_create_runtime", appRead, http.MethodPost, "/runtimes", false},
		{"app_write_cannot_put_runtime_settings", appWrite, http.MethodPut, "/cluster/runtime-settings", false},
		{"app_read_can_get_runtime_settings", appRead, http.MethodGet, "/cluster/runtime-settings", true},
		{"app_read_can_get_app_domain", appRead, http.MethodGet, "/cluster/app-domain", true},
		{"app_write_cannot_put_app_domain", appWrite, http.MethodPut, "/cluster/app-domain", false},
		{"app_read_can_get_postgres_availability", appRead, http.MethodGet, "/cluster/postgres-availability", true},
		{"app_write_cannot_put_postgres_availability", appWrite, http.MethodPut, "/cluster/postgres-availability", false},
		{"app_read_can_get_mysql_availability", appRead, http.MethodGet, "/cluster/mysql-availability", true},
		{"app_write_cannot_put_mysql_availability", appWrite, http.MethodPut, "/cluster/mysql-availability", false},
		{"app_read_can_get_redis_availability", appRead, http.MethodGet, "/cluster/redis-availability", true},
		{"app_write_cannot_put_redis_availability", appWrite, http.MethodPut, "/cluster/redis-availability", false},
		{"app_read_can_get_mongodb_availability", appRead, http.MethodGet, "/cluster/mongodb-availability", true},
		{"app_write_cannot_put_mongodb_availability", appWrite, http.MethodPut, "/cluster/mongodb-availability", false},
		{"app_read_can_get_kafka_availability", appRead, http.MethodGet, "/cluster/kafka-availability", true},
		{"app_write_cannot_put_kafka_availability", appWrite, http.MethodPut, "/cluster/kafka-availability", false},
		{"app_read_can_get_clickhouse_availability", appRead, http.MethodGet, "/cluster/clickhouse-availability", true},
		{"app_write_cannot_put_clickhouse_availability", appWrite, http.MethodPut, "/cluster/clickhouse-availability", false},
		{"cluster_key_can_create_runtime", clusterKey, http.MethodPost, "/runtimes", true},
		{"app_read_can_list_db_runtimes", appRead, http.MethodGet, "/db-runtimes", true},
		{"app_read_cannot_create_db_runtime", appRead, http.MethodPost, "/db-runtimes", false},
		{"cluster_key_can_create_db_runtime", clusterKey, http.MethodPost, "/db-runtimes", true},
		{"admin_can_create_db_runtime", adminBearer, http.MethodPost, "/db-runtimes", true},

		{"app_read_can_get_github_app", appRead, http.MethodGet, "/github/app", true},
		{"app_write_cannot_put_github_app", appWrite, http.MethodPut, "/github/app", false},
		{"admin_can_put_github_app", adminBearer, http.MethodPut, "/github/app", true},
		{"app_read_cannot_list_github_installations", appRead, http.MethodGet, "/github/installations", false},
		{"app_read_cannot_list_github_repos", appRead, http.MethodGet, "/github/installations/7/repos", false},
		{"app_read_cannot_head_github_installations", appRead, http.MethodHead, "/github/installations", false},
		{"github_read_cannot_list_github_installations", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:github:read"}}}}, http.MethodGet, "/github/installations", false},
		{"deploy_cannot_list_github_installations", appDeploy, http.MethodGet, "/github/installations", false},
		{"app_write_can_list_github_installations", appWrite, http.MethodGet, "/github/installations", true},
		{"app_write_can_list_github_repos", appWrite, http.MethodGet, "/github/installations/7/repos", true},
		{"github_write_can_list_github_installations", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:github:write"}}}}, http.MethodGet, "/github/installations", true},
		{"github_write_can_list_github_repos", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:github:write"}}}}, http.MethodGet, "/github/installations/7/repos", true},
		{"admin_can_list_github_installations", adminBearer, http.MethodGet, "/github/installations", true},
		{"app_write_can_put_app_github", appWrite, http.MethodPut, "/apps/app-1/github", true},
		{"app_read_cannot_put_app_github", appRead, http.MethodPut, "/apps/app-1/github", false},
		{"deploy_grant_can_github_deploy", appDeploy, http.MethodPost, "/apps/app-1/github/deploy", true},
		{"app_read_cannot_github_deploy", appRead, http.MethodPost, "/apps/app-1/github/deploy", false},

		{"logs_read_can_get_log", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodGet, "/apps/app-1/log", true},
		{"logs_read_can_get_job_output", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodGet, "/apps/app-1/jobs/job-1/output", true},
		{"logs_read_can_get_release_output", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodGet, "/apps/app-1/releases/rel-1/output", true},
		{"logs_read_cannot_put_release_output", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodPut, "/apps/app-1/releases/rel-1/output", false},
		{"jobs_read_cannot_get_job_output", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:read"}}}}, http.MethodGet, "/apps/app-1/jobs/job-1/output", false},
		{"activity_read_cannot_get_release_output", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:activity:read"}}}}, http.MethodGet, "/apps/app-1/releases/rel-1/output", false},
		{"app_write_can_put_release_output", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}}, http.MethodPut, "/apps/app-1/releases/rel-1/output", true},
		{"app_read_cannot_put_release_output", appRead, http.MethodPut, "/apps/app-1/releases/rel-1/output", false},
		{"logs_read_cannot_get_metrics", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:logs:read"}}}}, http.MethodGet, "/apps/app-1/jobs-stats", false},
		{"scale_write_can_put_formation", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:scale:write"}}}}, http.MethodPut, "/apps/app-1/formations/rel-1", true},
		{"scale_write_cannot_deploy", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:scale:write"}}}}, http.MethodPost, "/apps/app-1/deploy", false},
		{"jobs_run_can_post_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:run"}}}}, http.MethodPost, "/apps/app-1/jobs", true},
		{"jobs_run_cannot_put_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:run"}}}}, http.MethodPut, "/apps/app-1/jobs/job-1", false},
		{"jobs_run_cannot_patch_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:run"}}}}, http.MethodPatch, "/apps/app-1/jobs/job-1", false},
		{"app_write_cannot_put_job", appWrite, http.MethodPut, "/apps/app-1/jobs/job-1", false},
		{"cluster_key_can_put_job", clusterKey, http.MethodPut, "/apps/app-1/jobs/job-1", true},
		{"jobs_run_cannot_stop_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:run"}}}}, http.MethodDelete, "/apps/app-1/jobs/job-1", false},
		{"jobs_stop_can_delete_job", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:jobs:stop"}}}}, http.MethodDelete, "/apps/app-1/jobs/job-1", true},
		{"routes_write_can_post_route", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:routes:write"}}}}, http.MethodPost, "/apps/app-1/routes", true},
		{"env_write_can_put_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:env:write"}}}}, http.MethodPut, "/apps/app-1/release", true},
		{"env_write_can_post_cluster_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:env:write"}}}}, http.MethodPost, "/releases", true},
		{"scale_write_cannot_post_cluster_release", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:scale:write"}}}}, http.MethodPost, "/releases", false},

		{"user_can_list_apps", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/apps", true},
		{"user_can_create_app", &authorizer.Token{UserID: "u1"}, http.MethodPost, "/apps", true},
		{"user_can_get_providers", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/providers", true},
		{"app_read_can_get_provider_catalog", appRead, http.MethodGet, "/providers", true},
		{"app_read_cannot_provision", appRead, http.MethodPost, "/providers/postgres/resources", false},
		{"app_write_can_provision", appWrite, http.MethodPost, "/providers/postgres/resources", true},
		{"user_cannot_put_resource", &authorizer.Token{UserID: "u1"}, http.MethodPut, "/providers/postgres/resources/res-1", false},
		{"app_write_cannot_put_resource", appWrite, http.MethodPut, "/providers/postgres/resources/res-1", false},
		{"cluster_key_can_put_resource", clusterKey, http.MethodPut, "/providers/postgres/resources/res-1", true},
		{"user_cannot_update_provider", &authorizer.Token{UserID: "u1"}, http.MethodPut, "/providers/postgres", false},
		{"app_write_cannot_update_provider", appWrite, http.MethodPut, "/providers/postgres", false},
		{"cluster_key_can_update_provider", clusterKey, http.MethodPut, "/providers/postgres", true},
		{"user_can_attach_resource", &authorizer.Token{UserID: "u1"}, http.MethodPut, "/providers/postgres/resources/res-1/apps/app-1", true},
		{"user_can_get_resource", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/providers/postgres/resources/res-1", true},
		{"events_token_cannot_get_resource", &authorizer.Token{Scopes: []string{authz.ScopeEventsRead}, AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:events:read"}}}}, http.MethodGet, "/providers/postgres/resources/res-1", false},
		{"acme_token_cannot_get_resource", &authorizer.Token{Scopes: []string{authz.ScopeACMEIssue}}, http.MethodGet, "/providers/postgres/resources/res-1", false},
		{"user_can_get_tenancy", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/tenancy", true},
		{"user_can_get_placement", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/cluster/placement", true},
		{"app_read_cannot_put_placement", appRead, http.MethodPut, "/cluster/placement", false},
		{"cluster_key_can_put_placement", clusterKey, http.MethodPut, "/cluster/placement", true},
		{"user_cannot_get_capacity", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/cluster/capacity", false},
		{"app_read_cannot_get_capacity", appRead, http.MethodGet, "/cluster/capacity", false},
		{"cluster_key_can_get_capacity", clusterKey, http.MethodGet, "/cluster/capacity", true},
		{"cluster_key_can_list_tenant_backups", clusterKey, http.MethodGet, "/tenant-backups", true},
		{"cluster_key_can_begin_tenant_backup", clusterKey, http.MethodPost, "/tenant-backups/11111111-1111-1111-1111-111111111111/begin", true},
		{"app_read_cannot_list_tenant_backups", appRead, http.MethodGet, "/tenant-backups", false},
		{"app_write_cannot_begin_tenant_backup", appWrite, http.MethodPost, "/tenant-backups/11111111-1111-1111-1111-111111111111/begin", false},
		{"user_cannot_list_tenant_backups", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/tenant-backups", false},
		{"app_read_cannot_put_tenancy", appRead, http.MethodPut, "/tenancy", false},
		{"user_cannot_put_tenancy", &authorizer.Token{UserID: "u1"}, http.MethodPut, "/tenancy", false},
		{"admin_bearer_can_put_tenancy", adminBearer, http.MethodPut, "/tenancy", true},
		{"cluster_key_can_put_tenancy", clusterKey, http.MethodPut, "/tenancy", true},
		{"stranger_grant_cannot_get_other_app", wrongApp, http.MethodGet, "/apps/app-1", false},
		{"user_can_whoami", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/whoami", true},
		{"app_read_cannot_whoami", appRead, http.MethodGet, "/whoami", false},
		{"user_can_read_own_grants", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/whoami/grants", true},
		{"app_read_cannot_read_grants", appRead, http.MethodGet, "/whoami/grants", false},
		{"user_can_list_resources", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/resources", true},
		{"app_read_cannot_list_resources", appRead, http.MethodGet, "/resources", false},
		{"app_write_cannot_list_resources", appWrite, http.MethodGet, "/resources", false},
		{"user_cannot_post_resources", &authorizer.Token{UserID: "u1"}, http.MethodPost, "/resources", false},
		{"user_cannot_get_resources_subpath", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/resources/r1", false},
		{"cluster_key_can_list_resources", clusterKey, http.MethodGet, "/resources", true},
		{"user_can_get_plugin_catalog", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/plugin-catalog", true},
		{"app_read_cannot_get_plugin_catalog", appRead, http.MethodGet, "/plugin-catalog", false},
		{"user_cannot_post_plugin_catalog", &authorizer.Token{UserID: "u1"}, http.MethodPost, "/plugin-catalog", false},
		{"user_cannot_get_plugin_catalog_subpath", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/plugin-catalog/x", false},
		{"cluster_key_can_get_plugin_catalog", clusterKey, http.MethodGet, "/plugin-catalog", true},
		{"user_cannot_create_user", &authorizer.Token{UserID: "u1"}, http.MethodPost, "/users", false},
		{"cluster_admin_user_can_create_user", &authorizer.Token{UserID: "u1", Scopes: []string{"cluster:admin"}}, http.MethodPost, "/users", true},
		{"cluster_key_can_create_user", clusterKey, http.MethodPost, "/users", true},
		{"app_write_grant_cannot_create_user", appRead, http.MethodPost, "/users", false},
		{"user_cannot_list_users", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/users", false},
		{"user_cannot_patch_user", &authorizer.Token{UserID: "u1"}, http.MethodPatch, "/users/u2", false},
		{"cluster_key_can_patch_user", clusterKey, http.MethodPatch, "/users/u2", true},
		{"app_read_cannot_create_user", appRead, http.MethodPost, "/users", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := httpAllowed(tc.tok, tc.method, tc.path)
			if got != tc.allowed {
				t.Fatalf("httpAllowed(tok, %q, %q) = %v, want %v", tc.method, tc.path, got, tc.allowed)
			}
		})
	}
}

func TestAppScopedTokenForbiddenOnGETApps(t *testing.T) {
	// Dashboard grants are app-scoped and must not enumerate the cluster catalog.
	cases := [][]string{
		{"app:read"},
		{"app:write"},
		{"app:admin"},
		{"app:deploy"},
		{authz.ScopeBuildArtifacts},
	}
	for _, perms := range cases {
		tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: perms}}}
		if httpAllowed(tok, http.MethodGet, "/apps") {
			t.Fatalf("app-scoped %v must 403 on GET /apps", perms)
		}
	}
	if !httpAllowed(&authorizer.Token{UserID: "u1"}, http.MethodGet, "/apps") {
		t.Fatal("user tokens may GET /apps (handler then filters)")
	}
	if !httpAllowed(&authorizer.Token{ClusterKey: true}, http.MethodGet, "/apps") {
		t.Fatal("cluster key may GET /apps")
	}
}

// TestEventsReadScopeIsNotClusterAdmin is the dashboard events credential.
// It must reach GET /events so the handler can require app_id. It must not
// be cluster admin, and it must not read release env.
func TestEventsReadScopeIsNotClusterAdmin(t *testing.T) {
	tok := &authorizer.Token{
		UserID: "acct-a",
		Scopes: []string{"events:read"},
		AppGrants: []authorizer.AppGrant{{
			AppID:       "app-a",
			Permissions: []string{"events:read"},
		}},
	}
	if tok.ClusterKey || tok.HasClusterAdmin() {
		t.Fatal("events:read must not be cluster admin and must not set ClusterKey")
	}
	if authz.MayReadAppEnv(tok, "app-a", "app-a") {
		t.Fatal("events:read must not read release env")
	}
	if !httpAllowed(tok, http.MethodGet, "/events") {
		t.Fatal("events:read must reach GET /events")
	}
	if !httpAllowed(tok, http.MethodGet, "/events/42") {
		t.Fatal("events:read must reach GET /events/:id")
	}
	if httpAllowed(tok, http.MethodGet, "/apps/app-b") {
		t.Fatal("events:read must not read another app")
	}
	if httpAllowed(tok, http.MethodDelete, "/apps/app-a") || httpAllowed(tok, http.MethodPut, "/cluster/settings") {
		t.Fatal("events:read must not delete the app or change cluster settings")
	}
}

// TestGitreceiveBuildScopeIsNotClusterAdmin is the receiver credential.
// POST /apps/:id/builds and GET /artifacts/:id are reachable. Another app's
// env, deleting that app, and cluster settings are not. Listing artifacts
// stays cluster-only.
func TestGitreceiveBuildScopeIsNotClusterAdmin(t *testing.T) {
	tok := &authorizer.Token{
		UserID: "acct-a",
		Scopes: []string{"gitreceive:build"},
		AppGrants: []authorizer.AppGrant{{
			AppID:       "app-a",
			Permissions: []string{"app:write"},
		}},
	}
	if tok.ClusterKey || tok.HasClusterAdmin() {
		t.Fatal("gitreceive:build must not be cluster admin and must not set ClusterKey")
	}
	if !authz.MayReadAppEnv(tok, "app-a", "name-a") {
		t.Fatal("app:write on the app being built must read that app's env")
	}
	if authz.MayReadAppEnv(tok, "app-b", "name-b") {
		t.Fatal("gitreceive:build must not read another app's env")
	}
	if !httpAllowed(tok, http.MethodPost, "/apps/app-a/builds") {
		t.Fatal("gitreceive:build must reach POST /apps/:id/builds")
	}
	if !httpAllowed(tok, http.MethodGet, "/artifacts/art-1") {
		t.Fatal("gitreceive:build must reach GET /artifacts/:id")
	}
	if httpAllowed(tok, http.MethodGet, "/artifacts") || httpAllowed(tok, http.MethodPost, "/apps/app-a/builds/job/token") {
		t.Fatal("listing artifacts and minting build tokens stay cluster-only")
	}
	if httpAllowed(tok, http.MethodGet, "/apps/app-b/release") || httpAllowed(tok, http.MethodDelete, "/apps/app-b") || httpAllowed(tok, http.MethodPut, "/cluster/settings") {
		t.Fatal("gitreceive:build must not read another app, delete it, or change cluster settings")
	}
	admin := &authorizer.Token{UserID: "admin", Scopes: []string{"cluster:admin"}}
	if httpAllowed(admin, http.MethodPost, "/apps/app-a/builds") {
		t.Fatal("a cluster:admin JWT must not create builds")
	}
	key := &authorizer.Token{ClusterKey: true}
	if !httpAllowed(key, http.MethodPost, "/apps/app-a/builds") || !httpAllowed(key, http.MethodGet, "/artifacts") {
		t.Fatal("the cluster key must still create builds and list artifacts")
	}
}

func TestAccountQuotaScopeIsNotClusterAdmin(t *testing.T) {
	tok := &authorizer.Token{UserID: "billing", Scopes: []string{authz.ScopeAccountQuota}}
	if tok.ClusterKey || tok.HasClusterAdmin() || authz.MayReadAppEnv(tok, "app-a", "name-a") {
		t.Fatal("account:quota must not be cluster admin or read env")
	}
	if !httpAllowed(tok, http.MethodPut, "/accounts/user:a/quota") || !httpAllowed(tok, http.MethodPost, "/accounts/user:a/suspend") {
		t.Fatal("account:quota must reach quota and suspend")
	}
	if !httpAllowed(tok, http.MethodGet, "/apps") || !httpAllowed(tok, http.MethodGet, "/apps/app-a/formations") {
		t.Fatal("account:quota must list apps and read formations for metering")
	}
	if httpAllowed(tok, http.MethodGet, "/apps/app-a/release") || httpAllowed(tok, http.MethodPut, "/apps/app-a/formations/rel-1") || httpAllowed(tok, http.MethodPut, "/cluster/settings") {
		t.Fatal("account:quota must not read release env, change a formation, or change cluster settings")
	}
}

// TestQuotaTokenReadsFormations checks the billing meter's route. The
// controller serves GET /apps/:id/formations; there is no /formation route.
func TestQuotaTokenReadsFormations(t *testing.T) {
	tok := &authorizer.Token{UserID: "billing", Scopes: []string{authz.ScopeAccountQuota}}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if !httpAllowed(tok, m, "/apps/app-a/formations") {
			t.Fatalf("account:quota must %s /apps/:id/formations", m)
		}
	}
	for _, route := range [][2]string{
		{http.MethodGet, "/apps/app-a/formation"},
		{http.MethodPut, "/apps/app-a/formations/rel-1"},
		{http.MethodDelete, "/apps/app-a/formations/rel-1"},
		{http.MethodGet, "/apps/app-a/formations/rel-1"},
		{http.MethodGet, "/apps/app-a/release"},
		{http.MethodGet, "/apps/app-a/releases"},
		{http.MethodGet, "/apps/app-a/jobs"},
	} {
		if httpAllowed(tok, route[0], route[1]) {
			t.Fatalf("account:quota allowed on %s %s", route[0], route[1])
		}
	}
}

func TestACMEIssueScopeIsNotClusterAdmin(t *testing.T) {
	tok := &authorizer.Token{UserID: "acme", Scopes: []string{authz.ScopeACMEIssue}}
	if tok.ClusterKey || tok.HasClusterAdmin() || authz.MayReadAppEnv(tok, "app-a", "name-a") {
		t.Fatal("acme:issue must not be cluster admin or read env")
	}
	if !httpAllowed(tok, http.MethodGet, "/managed-certificates") || !httpAllowed(tok, http.MethodPut, "/managed-certificates/cert-1") {
		t.Fatal("acme:issue must list and update managed certificates")
	}
	if !httpAllowed(tok, http.MethodGet, "/acme/config") || !httpAllowed(tok, http.MethodPut, "/apps/app-a/routes/http/r1") {
		t.Fatal("acme:issue must read ACME config and update a route")
	}
	if httpAllowed(tok, http.MethodPost, "/apps/app-a/routes") || httpAllowed(tok, http.MethodDelete, "/apps/app-a/routes/http/r1") {
		t.Fatal("acme:issue must not create or delete routes (NEW-30)")
	}
	if httpAllowed(tok, http.MethodPut, "/acme/config") || httpAllowed(tok, http.MethodPut, "/apps/app-a/formation") || httpAllowed(tok, http.MethodGet, "/apps/app-a/release") {
		t.Fatal("acme:issue must not change ACME config, formations, or release env")
	}
}

func TestBuildCredentialIsNotUserSession(t *testing.T) {
	grants := []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{authz.PermAppWrite}}}
	for _, scope := range []string{authz.ScopeBuildArtifacts, authz.ScopeGitreceiveBuild} {
		tok := &authorizer.Token{UserID: "build:owner", Scopes: []string{scope}, AppGrants: grants}
		if authz.UserBound(tok) {
			t.Fatalf("%s token with a user id must not be user-bound", scope)
		}
		for _, route := range [][2]string{{"POST", "/tokens"}, {"GET", "/tokens"}, {"GET", "/whoami"}, {"GET", "/whoami/grants"}, {"GET", "/resources"}, {"GET", "/plugin-catalog"}, {"POST", "/apps/app-1/transfer"}} {
			if httpAllowed(tok, route[0], route[1]) {
				t.Fatalf("%s token allowed on %s %s", scope, route[0], route[1])
			}
		}
	}
	if !authz.UserBound(&authorizer.Token{UserID: "u1"}) {
		t.Fatal("a plain user token is user-bound")
	}
	if !httpAllowed(&authorizer.Token{UserID: "u1"}, "POST", "/tokens") {
		t.Fatal("a user may mint a PAT")
	}
}

func TestServiceScopesDenyUserSessionRoutes(t *testing.T) {
	for _, scope := range []string{authz.ScopeACMEIssue, authz.ScopeAccountQuota} {
		tok := &authorizer.Token{UserID: "svc", Scopes: []string{scope}}
		if authz.UserBound(tok) {
			t.Fatalf("%s token must not be user-bound", scope)
		}
		for _, route := range [][2]string{
			{"POST", "/users"},
			{"POST", "/apps"},
			{"POST", "/tokens"},
			{"GET", "/tokens"},
			{"GET", "/whoami"},
			{"GET", "/handles/x"},
			{"GET", "/domains"},
			{"POST", "/apps/app-1/transfer"},
			{"POST", "/apps/app-1/collaborators"},
			{"DELETE", "/providers/p1/resources/r1"},
			{"POST", "/providers/p1/resources"},
			{"GET", "/providers/p1/resources/r1"},
		} {
			if httpAllowed(tok, route[0], route[1]) {
				t.Fatalf("%s token allowed on %s %s", scope, route[0], route[1])
			}
		}
	}
	// The routes each scope exists for still work.
	quota := &authorizer.Token{UserID: "billing", Scopes: []string{authz.ScopeAccountQuota}}
	if !httpAllowed(quota, "GET", "/apps") || !httpAllowed(quota, "PUT", "/accounts/user:a/quota") {
		t.Fatal("account:quota must keep its own routes")
	}
	acme := &authorizer.Token{UserID: "acme", Scopes: []string{authz.ScopeACMEIssue}}
	if !httpAllowed(acme, "GET", "/routes") || !httpAllowed(acme, "GET", "/apps") || !httpAllowed(acme, "PUT", "/managed-certificates/c1") {
		t.Fatal("acme:issue must keep its own routes")
	}
}

// TestBuildLockIsClusterOnly (EDGE-09): the build lease is gitreceive's and
// flynn-receiver's, with the cluster key. An app admin or a receive token
// holding it could block every deploy of the app.
func TestBuildLockIsClusterOnly(t *testing.T) {
	owner := &authorizer.Token{
		UserID: "acct-a",
		AppGrants: []authorizer.AppGrant{{
			AppID:       "app-a",
			Permissions: []string{"app:admin", "app:write", "app:deploy"},
		}},
	}
	receive := &authorizer.Token{
		UserID:    "acct-a",
		Scopes:    []string{"gitreceive:build"},
		AppGrants: []authorizer.AppGrant{{AppID: "app-a", Permissions: []string{"app:write"}}},
	}
	key := &authorizer.Token{ClusterKey: true}
	for _, req := range []struct{ method, path string }{
		{http.MethodPut, "/apps/app-a/build-lock"},
		{http.MethodDelete, "/apps/app-a/build-lock/holder-1"},
		// keyed leases (EDGE-40): a pipeline's preview remote
		{http.MethodPut, "/build-locks/pipeline:shop"},
		{http.MethodDelete, "/build-locks/pipeline:shop/holder-1"},
	} {
		if httpAllowed(owner, req.method, req.path) || httpAllowed(receive, req.method, req.path) {
			t.Fatalf("%s %s is open to an app grant", req.method, req.path)
		}
		if !httpAllowed(key, req.method, req.path) {
			t.Fatalf("%s %s refused the cluster key", req.method, req.path)
		}
	}
}
func TestRegionRouteAuth(t *testing.T) {
	tenant := &authorizer.Token{UserID: "u1", AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:write"}}}}
	key := &authorizer.Token{ClusterKey: true}

	if !httpAllowed(tenant, http.MethodGet, "/cluster/regions") {
		t.Fatal("a tenant must be able to list regions")
	}
	if httpAllowed(tenant, http.MethodPost, "/cluster/regions") {
		t.Fatal("a tenant must not link an arbitrary URL")
	}
	if httpAllowed(tenant, http.MethodDelete, "/cluster/regions/EU") {
		t.Fatal("a tenant must not unlink a region")
	}
	if httpAllowed(tenant, http.MethodPut, "/cluster/region") {
		t.Fatal("a tenant must not set the cluster region")
	}
	if !httpAllowed(key, http.MethodPost, "/cluster/regions") {
		t.Fatal("the cluster key must link with no user account")
	}
	if !httpAllowed(key, http.MethodPut, "/cluster/region") {
		t.Fatal("the cluster key must set the region at bootstrap")
	}
	if !preAuthPublic(http.MethodGet, "/cluster/region") {
		t.Fatal("the peer dial reads GET /cluster/region with no cluster key")
	}
	if preAuthPublic(http.MethodPost, "/cluster/region") || preAuthPublic(http.MethodGet, "/cluster/regions") {
		t.Fatal("only GET /cluster/region is public")
	}
}
func TestDefaultAppRolesHTTP(t *testing.T) {
	tok := func(roleID string) *authorizer.Token {
		r, ok := authz.DefaultRoleByID(roleID)
		if !ok {
			t.Fatalf("missing role %s", roleID)
		}
		return &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: r.Permissions}}}
	}

	cases := []struct {
		role    string
		method  string
		path    string
		allowed bool
	}{
		{"view", http.MethodGet, "/apps/app-1", true},
		{"view", http.MethodPost, "/apps/app-1/deploy", false},
		{"view", http.MethodDelete, "/apps/app-1/releases/rel-1", false},
		{"deploy", http.MethodGet, "/apps/app-1", true},
		{"deploy", http.MethodPost, "/apps/app-1/deploy", true},
		{"deploy", http.MethodDelete, "/apps/app-1/releases/rel-1", false},
		{"deploy", http.MethodPut, "/apps/app-1/scale/rel-1", false},
		{"manage", http.MethodGet, "/apps/app-1", true},
		{"manage", http.MethodPost, "/apps/app-1/deploy", true},
		{"manage", http.MethodDelete, "/apps/app-1/releases/rel-1", true},
		{"manage", http.MethodPut, "/apps/app-1/scale/rel-1", true},
		{"manage", http.MethodPost, "/releases", true},
		{"deploy", http.MethodPost, "/releases", false},
		{"view", http.MethodPost, "/releases", false},
		{"admin", http.MethodPost, "/releases", true},
		{"admin", http.MethodGet, "/apps/app-1", true},
		{"admin", http.MethodPost, "/apps/app-1/deploy", true},
		{"admin", http.MethodDelete, "/apps/app-1/releases/rel-1", true},
		{"admin", http.MethodDelete, "/apps/app-1", true},
	}
	for _, tc := range cases {
		t.Run(tc.role+"_"+tc.method+"_"+tc.path, func(t *testing.T) {
			got := httpAllowed(tok(tc.role), tc.method, tc.path)
			if got != tc.allowed {
				t.Fatalf("httpAllowed(%s, %s %s) = %v, want %v", tc.role, tc.method, tc.path, got, tc.allowed)
			}
		})
	}
}

// A request path may name the app by name or id. The grant is by id, so the
// route check resolves the path segment first.
func TestRouteAuthzResolvesPathRefToID(t *testing.T) {
	const victim = "11111111-2222-3333-4444-555555555555"
	resolve := func(ref string) string {
		switch ref {
		case "demo":
			return "id-demo"
		case victim:
			return "id-victim"
		}
		return ref
	}
	own := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "id-demo", Permissions: []string{"app:write"}}}}
	if !httpAllowedResolved(own, http.MethodGet, "/apps/demo/releases", resolve) {
		t.Error("by name: own app must be allowed")
	}
	if !httpAllowedResolved(own, http.MethodGet, "/apps/id-demo/releases", resolve) {
		t.Error("by id: own app must be allowed")
	}
	// A legacy app whose name is the victim's id carries a grant under that
	// name; resolving the path to the victim's real id must not match it.
	legacy := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: victim, Permissions: []string{"app:write"}}}}
	if httpAllowedResolved(legacy, http.MethodGet, "/apps/"+victim+"/releases", resolve) {
		t.Error("a grant keyed by another app's id must not cover the victim app")
	}
}

// acmeIssueRoutes is every route the acme:issue token needs: the
// letsencrypt plugin lists apps and routes and turns HTTPS on with a route
// PUT; the ACME worker (router/acme) lists, streams and updates managed
// certificates and reads the ACME config.
var acmeIssueRoutes = map[string]bool{
	"GET /apps":   true,
	"GET /routes": true,
	"PUT /apps/:apps_id/routes/:routes_type/:routes_id": true,
	"GET /managed-certificates":                         true,
	"PUT /managed-certificates/:managed_certificate_id": true,
	"GET /acme/config":                                  true,
}

// TestACMEIssueReachesOnlyItsRoutes walks every registered route: the
// acme:issue token gets in on exactly acmeIssueRoutes and nowhere else
// (NEW-30).
func TestACMEIssueReachesOnlyItsRoutes(t *testing.T) {
	tok := &authorizer.Token{UserID: "acme", Scopes: []string{authz.ScopeACMEIssue}}
	seen := map[string]bool{}
	for _, r := range registeredTestRoutes(t).routes {
		key := r.Method + " " + r.Pattern
		if r.Req.IsPublic() {
			continue
		}
		for _, path := range expandPattern(r.Pattern) {
			if strings.HasPrefix(path, "/apps/controller") {
				continue
			}
			got := httpAllowed(tok, r.Method, path)
			if got != acmeIssueRoutes[key] {
				t.Errorf("acme:issue %s %s = %v, want %v", r.Method, path, got, acmeIssueRoutes[key])
			}
		}
		seen[key] = true
	}
	for key := range acmeIssueRoutes {
		if !seen[key] {
			t.Errorf("%s is not a registered route", key)
		}
	}
}
