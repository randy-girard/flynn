package plugin

import (
	"fmt"
	"os"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

// appReleaseGetter is the ClusterEnv subset of the controller client.
type appReleaseGetter interface {
	GetAppRelease(appID string) (*ct.Release, error)
}

// ReleaseEnv builds the system-app release environment from the manifest and
// the cluster. image_env values equal to "self" become artifactID.
func ReleaseEnv(m *Manifest, artifactID string, cluster map[string]string) map[string]string {
	env := map[string]string{}
	for k, v := range m.Env {
		env[k] = ExpandClusterVars(v, cluster)
	}
	for _, key := range m.InjectEnv {
		if v, ok := cluster[key]; ok {
			env[key] = v
		}
	}
	for k, v := range m.ImageEnv {
		if v == ImageSelf {
			env[k] = artifactID
			continue
		}
		env[k] = ExpandClusterVars(v, cluster)
	}
	for _, key := range m.GenerateEnv {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if env[key] == "" {
			if v := cluster[key]; v != "" {
				env[key] = v
			} else {
				env[key] = random.Hex(16)
			}
		}
	}
	if m != nil {
		for _, p := range m.Setup {
			if v := cluster[p.Env]; v != "" {
				env[p.Env] = v
			}
		}
	}
	for _, k := range []string{"DATABASE_URL", "PGHOST", "PGUSER", "PGPASSWORD", "PGDATABASE"} {
		if env[k] == "" && cluster[k] != "" {
			env[k] = cluster[k]
		}
	}
	injectGitHubToken(env, "")
	return env
}

// injectGitHubToken copies the host GitHub token onto plugin release env so
// jobs (dashboard update checks) can list private releases. TokenForHost
// reads process env then /etc/flynn/plugin-credentials.json. The token is
// never logged. GITHUB_TOKEN is set only when empty so an existing value is
// kept.
func injectGitHubToken(env map[string]string, credsFile string) {
	if env == nil {
		return
	}
	token, _, err := TokenForHost(DefaultGitHubHost, credsFile)
	if err != nil || token == "" {
		return
	}
	env[EnvGitHubToken] = token
	if env[EnvGitHubTokenAlt] == "" {
		env[EnvGitHubTokenAlt] = token
	}
}

// clusterAuthEnvKeys are copied onto plugin releases on install/update when
// empty so older plugin apps pick up CONTROLLER_KEY / DISCOVERD_AUTH_KEY
// without a manual env:set.
var clusterAuthEnvKeys = []string{
	"CONTROLLER_KEY",
	"DISCOVERD_AUTH_KEY",
	"ACCESS_TOKEN_KEY",
	"ACCESS_TOKEN_SIGNING_KEY",
	"ACCESS_TOKEN_PRIVATE_KEY",
}

// EnsureClusterAuthEnv copies missing cluster secrets onto env. It never
// overwrites a non-empty value. AUTH_KEY is used only as a CONTROLLER_KEY
// fallback (controller releases historically store the cluster key there).
func EnsureClusterAuthEnv(env, cluster map[string]string) bool {
	if env == nil || cluster == nil {
		return false
	}
	changed := false
	for _, k := range clusterAuthEnvKeys {
		if env[k] == "" && cluster[k] != "" {
			env[k] = cluster[k]
			changed = true
		}
	}
	if env["CONTROLLER_KEY"] == "" && cluster["AUTH_KEY"] != "" {
		env["CONTROLLER_KEY"] = cluster["AUTH_KEY"]
		changed = true
	}
	return changed
}

// PreservePreviousEnv copies env keys from a previous release that the new
// release did not set (for example DATABASE_URL from a provisioned resource).
func PreservePreviousEnv(env, previous map[string]string) {
	if env == nil || previous == nil {
		return
	}
	for k, v := range previous {
		if v != "" && env[k] == "" {
			env[k] = v
		}
	}
}

// PreserveGeneratedEnv copies previously generated secrets onto env so a
// plugin upgrade does not rotate MYSQL_PWD / MONGO_PWD out from under a
// running cluster.
func PreserveGeneratedEnv(m *Manifest, env, previous map[string]string) {
	if m == nil || env == nil || previous == nil {
		return
	}
	for _, key := range m.GenerateEnv {
		if v := previous[key]; v != "" {
			env[key] = v
		}
	}
	for _, p := range m.Setup {
		if !p.Generate {
			continue
		}
		if v := previous[p.Env]; v != "" {
			env[p.Env] = v
		}
	}
}

// FormationScale is the install formation. Manifest app.scale wins per
// process; other processes use SingletonWebCount.
func FormationScale(m *Manifest, cluster map[string]string) map[string]int {
	n := SingletonWebCount(cluster)
	procs := map[string]int{}
	if m == nil {
		return procs
	}
	for name := range m.App.Processes {
		if m.App.Scale != nil {
			if s, ok := m.App.Scale[name]; ok {
				procs[name] = s
				continue
			}
		}
		procs[name] = n
	}
	return procs
}

// previousReleaseScaleDown is the formation used to stop jobs from the
// release that plugin install just replaced. ScaleAppRelease only updates
// the new release; without this, the old formation stays at web=1 and
// discoverd keeps both backends (HTML from vN, JS from vN-1 → 404s).
//
// Plugin updates apply this *before* scaling the new release. A singleton
// host cannot place a second dashboard (or similar) web job while the old
// web=1 job still holds RAM; waiting on the new formation looks hung after
// "resource postgres already attached".
func previousReleaseScaleDown(prev *ct.Release, formation *ct.Formation) map[string]int {
	zeros := map[string]int{}
	if formation != nil {
		for name := range formation.Processes {
			zeros[name] = 0
		}
	}
	if prev != nil {
		for name := range prev.Processes {
			zeros[name] = 0
		}
	}
	return zeros
}

// ClusterEnv reads well-known secrets from already-running core apps
// (controller/postgres). Dashboard webhook credentials are copied when the
// dashboard plugin is already installed so other plugins can post metrics;
// DATABASE_URL is never copied from dashboard.
func ClusterEnv(client appReleaseGetter) (map[string]string, error) {
	out := map[string]string{}
	for _, app := range []string{"controller", "postgres", "gitreceive", "discoverd"} {
		release, err := client.GetAppRelease(app)
		if err != nil {
			continue
		}
		if key := release.Env["CONTROLLER_KEY"]; key != "" {
			out["CONTROLLER_KEY"] = key
		}
		if key := release.Env["AUTH_KEY"]; key != "" && out["CONTROLLER_KEY"] == "" {
			out["CONTROLLER_KEY"] = key
		}
		if v := release.Env["DISCOVERD_AUTH_KEY"]; v != "" {
			out["DISCOVERD_AUTH_KEY"] = v
		}
		if v := release.Env["SINGLETON"]; v != "" {
			out["SINGLETON"] = v
		}
		for _, k := range []string{
			"CLUSTER_DOMAIN", "DEFAULT_ROUTE_DOMAIN",
			"ACCESS_TOKEN_KEY", "ACCESS_TOKEN_SIGNING_KEY",
			"GIT_URL", "IMAGE_URL",
		} {
			if v := release.Env[k]; v != "" {
				out[k] = v
			}
		}
		if v := release.Env["ACCESS_TOKEN_SIGNING_KEY"]; v != "" {
			out["ACCESS_TOKEN_PRIVATE_KEY"] = v
		}
		for _, k := range []string{"CLUSTER_DOMAIN", "DEFAULT_ROUTE_DOMAIN"} {
			if v := release.Env[k]; v != "" {
				out[k] = v
				if k == "DEFAULT_ROUTE_DOMAIN" && out["CLUSTER_DOMAIN"] == "" {
					out["CLUSTER_DOMAIN"] = v
				}
			}
		}
	}
	// flynn-host plugin:install loads host.json into the process. vagrant-dev
	// may only have DISCOVERD_AUTH_KEY there, not on the discoverd app release.
	if out["DISCOVERD_AUTH_KEY"] == "" {
		if v := os.Getenv("DISCOVERD_AUTH_KEY"); v != "" {
			out["DISCOVERD_AUTH_KEY"] = v
		}
	}
	if out["CONTROLLER_KEY"] == "" {
		return nil, fmt.Errorf("unable to find CONTROLLER_KEY in controller or postgres release")
	}
	if out["SINGLETON"] == "" {
		out["SINGLETON"] = "false"
	}
	// Flynn bootstrap never sets GIT_URL / IMAGE_URL on core apps. Derive the
	// public gitreceive and tarreceive URLs from CLUSTER_DOMAIN so plugins
	// (dashboard Cluster settings) are not installed with empty values.
	if domain := strings.TrimSpace(out["CLUSTER_DOMAIN"]); domain != "" {
		if strings.TrimSpace(out["GIT_URL"]) == "" {
			out["GIT_URL"] = "https://git." + domain
		}
		if strings.TrimSpace(out["IMAGE_URL"]) == "" {
			out["IMAGE_URL"] = "https://images." + domain
		}
	}
	mergeDashboardPluginEnv(out, client)
	return out, nil
}

// mergeDashboardPluginEnv copies the dashboard webhook secret onto cluster env
// so other plugins can POST plugin-metrics. It never copies DATABASE_URL:
// that belongs only to the dashboard app (metrics history lives there).
func mergeDashboardPluginEnv(out map[string]string, client appReleaseGetter) {
	if out == nil || client == nil {
		return
	}
	for _, name := range []string{"dashboard-plugin", "dashboard"} {
		release, err := client.GetAppRelease(name)
		if err != nil || release == nil || len(release.Env) == 0 {
			continue
		}
		secret := strings.TrimSpace(release.Env["WEBHOOK_INGEST_SECRET"])
		if secret == "" {
			secret = strings.TrimSpace(release.Env["DASHBOARD_METRICS_SECRET"])
		}
		if secret != "" && out["DASHBOARD_METRICS_SECRET"] == "" {
			out["DASHBOARD_METRICS_SECRET"] = secret
		}
		if out["DASHBOARD_METRICS_URL"] == "" {
			if v := strings.TrimSpace(release.Env["DASHBOARD_METRICS_URL"]); v != "" {
				out["DASHBOARD_METRICS_URL"] = v
			} else {
				out["DASHBOARD_METRICS_URL"] = "http://dashboard.discoverd/webhooks/plugin-metrics"
			}
		}
		if out["DASHBOARD_SSO_JWKS_URL"] == "" {
			if v := strings.TrimSpace(release.Env["DASHBOARD_SSO_JWKS_URL"]); v != "" {
				out["DASHBOARD_SSO_JWKS_URL"] = v
			} else {
				out["DASHBOARD_SSO_JWKS_URL"] = "http://dashboard.discoverd/.well-known/jwks.json"
			}
		}
		return
	}
}

func SingletonWebCount(cluster map[string]string) int {
	if strings.EqualFold(cluster["SINGLETON"], "true") {
		return 1
	}
	return 2
}
