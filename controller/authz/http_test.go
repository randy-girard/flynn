package authz

import (
	"net/http"
	"testing"

	"github.com/randy-girard/flynn/controller/authorizer"
)

func TestHTTPAllowed(t *testing.T) {
	clusterKey := &authorizer.Token{ClusterKey: true}
	emptyTok := &authorizer.Token{}
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
		{"empty_token_cannot_list_apps", emptyTok, http.MethodGet, "/apps", false},
		{"empty_token_cannot_get_app", emptyTok, http.MethodGet, "/apps/app-1", false},
		{"scoped_admin_providers", adminBearer, http.MethodGet, "/providers", true},

		{"app_read_can_get_app", appRead, http.MethodGet, "/apps/app-1", true},
		{"app_read_head_app", appRead, http.MethodHead, "/apps/app-1", true},
		{"app_read_cannot_post_subresource", appRead, http.MethodPost, "/apps/app-1/releases", false},
		{"app_read_cannot_list_apps", appRead, http.MethodGet, "/apps", false},
		{"app_write_cannot_list_apps", appWrite, http.MethodGet, "/apps", false},
		{"app_admin_cannot_list_apps", &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:admin"}}}}, http.MethodGet, "/apps", false},
		{"app_read_cannot_head_apps", appRead, http.MethodHead, "/apps", false},

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
		{"events_token_cannot_get_resource", &authorizer.Token{Scopes: []string{ScopeEventsRead}, AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{"app:events:read"}}}}, http.MethodGet, "/providers/postgres/resources/res-1", false},
		{"acme_token_cannot_get_resource", &authorizer.Token{Scopes: []string{ScopeACMEIssue}}, http.MethodGet, "/providers/postgres/resources/res-1", false},
		{"user_can_get_tenancy", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/tenancy", true},
		{"user_can_get_placement", &authorizer.Token{UserID: "u1"}, http.MethodGet, "/cluster/placement", true},
		{"app_read_cannot_put_placement", appRead, http.MethodPut, "/cluster/placement", false},
		{"cluster_key_can_put_placement", clusterKey, http.MethodPut, "/cluster/placement", true},
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
			got := HTTPAllowed(tc.tok, tc.method, tc.path)
			if got != tc.allowed {
				t.Fatalf("HTTPAllowed(tok, %q, %q) = %v, want %v", tc.method, tc.path, got, tc.allowed)
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
		{ScopeBuildArtifacts},
	}
	for _, perms := range cases {
		tok := &authorizer.Token{AppGrants: []authorizer.AppGrant{{AppID: "app-1", Permissions: perms}}}
		if HTTPAllowed(tok, http.MethodGet, "/apps") {
			t.Fatalf("app-scoped %v must 403 on GET /apps", perms)
		}
	}
	if !HTTPAllowed(&authorizer.Token{UserID: "u1"}, http.MethodGet, "/apps") {
		t.Fatal("user tokens may GET /apps (handler then filters)")
	}
	if !HTTPAllowed(&authorizer.Token{ClusterKey: true}, http.MethodGet, "/apps") {
		t.Fatal("cluster key may GET /apps")
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
	if MayReadAppEnv(tok, "app-a", "app-a") {
		t.Fatal("events:read must not read release env")
	}
	if !HTTPAllowed(tok, http.MethodGet, "/events") {
		t.Fatal("events:read must reach GET /events")
	}
	if !HTTPAllowed(tok, http.MethodGet, "/events/42") {
		t.Fatal("events:read must reach GET /events/:id")
	}
	if HTTPAllowed(tok, http.MethodGet, "/apps/app-b") {
		t.Fatal("events:read must not read another app")
	}
	if HTTPAllowed(tok, http.MethodDelete, "/apps/app-a") || HTTPAllowed(tok, http.MethodPut, "/cluster/settings") {
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
	if !MayReadAppEnv(tok, "app-a", "name-a") {
		t.Fatal("app:write on the app being built must read that app's env")
	}
	if MayReadAppEnv(tok, "app-b", "name-b") {
		t.Fatal("gitreceive:build must not read another app's env")
	}
	if !HTTPAllowed(tok, http.MethodPost, "/apps/app-a/builds") {
		t.Fatal("gitreceive:build must reach POST /apps/:id/builds")
	}
	if !HTTPAllowed(tok, http.MethodGet, "/artifacts/art-1") {
		t.Fatal("gitreceive:build must reach GET /artifacts/:id")
	}
	if HTTPAllowed(tok, http.MethodGet, "/artifacts") || HTTPAllowed(tok, http.MethodPost, "/apps/app-a/builds/job/token") {
		t.Fatal("listing artifacts and minting build tokens stay cluster-only")
	}
	if HTTPAllowed(tok, http.MethodGet, "/apps/app-b/release") || HTTPAllowed(tok, http.MethodDelete, "/apps/app-b") || HTTPAllowed(tok, http.MethodPut, "/cluster/settings") {
		t.Fatal("gitreceive:build must not read another app, delete it, or change cluster settings")
	}
	admin := &authorizer.Token{UserID: "admin", Scopes: []string{"cluster:admin"}}
	if HTTPAllowed(admin, http.MethodPost, "/apps/app-a/builds") {
		t.Fatal("a cluster:admin JWT must not create builds")
	}
	key := &authorizer.Token{ClusterKey: true}
	if !HTTPAllowed(key, http.MethodPost, "/apps/app-a/builds") || !HTTPAllowed(key, http.MethodGet, "/artifacts") {
		t.Fatal("the cluster key must still create builds and list artifacts")
	}
}

func TestAccountQuotaScopeIsNotClusterAdmin(t *testing.T) {
	tok := &authorizer.Token{UserID: "billing", Scopes: []string{ScopeAccountQuota}}
	if tok.ClusterKey || tok.HasClusterAdmin() || MayReadAppEnv(tok, "app-a", "name-a") {
		t.Fatal("account:quota must not be cluster admin or read env")
	}
	if !HTTPAllowed(tok, http.MethodPut, "/accounts/user:a/quota") || !HTTPAllowed(tok, http.MethodPost, "/accounts/user:a/suspend") {
		t.Fatal("account:quota must reach quota and suspend")
	}
	if !HTTPAllowed(tok, http.MethodGet, "/apps") || !HTTPAllowed(tok, http.MethodGet, "/apps/app-a/formations") {
		t.Fatal("account:quota must list apps and read formations for metering")
	}
	if HTTPAllowed(tok, http.MethodGet, "/apps/app-a/release") || HTTPAllowed(tok, http.MethodPut, "/apps/app-a/formations/rel-1") || HTTPAllowed(tok, http.MethodPut, "/cluster/settings") {
		t.Fatal("account:quota must not read release env, change a formation, or change cluster settings")
	}
}

// TestQuotaTokenReadsFormations checks the billing meter's route. The
// controller serves GET /apps/:id/formations; there is no /formation route.
func TestQuotaTokenReadsFormations(t *testing.T) {
	tok := &authorizer.Token{UserID: "billing", Scopes: []string{ScopeAccountQuota}}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if !HTTPAllowed(tok, m, "/apps/app-a/formations") {
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
		if HTTPAllowed(tok, route[0], route[1]) {
			t.Fatalf("account:quota allowed on %s %s", route[0], route[1])
		}
	}
}

func TestACMEIssueScopeIsNotClusterAdmin(t *testing.T) {
	tok := &authorizer.Token{UserID: "acme", Scopes: []string{ScopeACMEIssue}}
	if tok.ClusterKey || tok.HasClusterAdmin() || MayReadAppEnv(tok, "app-a", "name-a") {
		t.Fatal("acme:issue must not be cluster admin or read env")
	}
	if !HTTPAllowed(tok, http.MethodGet, "/managed-certificates") || !HTTPAllowed(tok, http.MethodPut, "/managed-certificates/cert-1") {
		t.Fatal("acme:issue must list and update managed certificates")
	}
	if !HTTPAllowed(tok, http.MethodGet, "/acme/config") || !HTTPAllowed(tok, http.MethodPost, "/apps/app-a/routes") || !HTTPAllowed(tok, http.MethodDelete, "/apps/app-a/routes/http/r1") {
		t.Fatal("acme:issue must read ACME config and update routes")
	}
	if HTTPAllowed(tok, http.MethodPut, "/acme/config") || HTTPAllowed(tok, http.MethodPut, "/apps/app-a/formation") || HTTPAllowed(tok, http.MethodGet, "/apps/app-a/release") {
		t.Fatal("acme:issue must not change ACME config, formations, or release env")
	}
}

func TestBuildCredentialIsNotUserSession(t *testing.T) {
	grants := []authorizer.AppGrant{{AppID: "app-1", Permissions: []string{PermAppWrite}}}
	for _, scope := range []string{ScopeBuildArtifacts, ScopeGitreceiveBuild} {
		tok := &authorizer.Token{UserID: "build:owner", Scopes: []string{scope}, AppGrants: grants}
		if UserBound(tok) {
			t.Fatalf("%s token with a user id must not be user-bound", scope)
		}
		for _, route := range [][2]string{{"POST", "/tokens"}, {"GET", "/tokens"}, {"GET", "/whoami"}, {"GET", "/whoami/grants"}, {"GET", "/resources"}, {"GET", "/plugin-catalog"}, {"POST", "/apps/app-1/transfer"}} {
			if HTTPAllowed(tok, route[0], route[1]) {
				t.Fatalf("%s token allowed on %s %s", scope, route[0], route[1])
			}
		}
	}
	if !UserBound(&authorizer.Token{UserID: "u1"}) {
		t.Fatal("a plain user token is user-bound")
	}
	if !HTTPAllowed(&authorizer.Token{UserID: "u1"}, "POST", "/tokens") {
		t.Fatal("a user may mint a PAT")
	}
}

func TestServiceScopesDenyUserSessionRoutes(t *testing.T) {
	for _, scope := range []string{ScopeACMEIssue, ScopeAccountQuota} {
		tok := &authorizer.Token{UserID: "svc", Scopes: []string{scope}}
		if UserBound(tok) {
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
			if HTTPAllowed(tok, route[0], route[1]) {
				t.Fatalf("%s token allowed on %s %s", scope, route[0], route[1])
			}
		}
	}
	// The routes each scope exists for still work.
	quota := &authorizer.Token{UserID: "billing", Scopes: []string{ScopeAccountQuota}}
	if !HTTPAllowed(quota, "GET", "/apps") || !HTTPAllowed(quota, "PUT", "/accounts/user:a/quota") {
		t.Fatal("account:quota must keep its own routes")
	}
	acme := &authorizer.Token{UserID: "acme", Scopes: []string{ScopeACMEIssue}}
	if !HTTPAllowed(acme, "GET", "/routes") || !HTTPAllowed(acme, "GET", "/apps") || !HTTPAllowed(acme, "PUT", "/managed-certificates/c1") {
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
		if HTTPAllowed(owner, req.method, req.path) || HTTPAllowed(receive, req.method, req.path) {
			t.Fatalf("%s %s is open to an app grant", req.method, req.path)
		}
		if !HTTPAllowed(key, req.method, req.path) {
			t.Fatalf("%s %s refused the cluster key", req.method, req.path)
		}
	}
}
