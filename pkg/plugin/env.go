package plugin

import (
	"fmt"
	"strings"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/random"
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
		env[k] = v
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
		env[k] = v
	}
	for _, key := range m.GenerateEnv {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if env[key] == "" {
			env[key] = random.Hex(16)
		}
	}
	return env
}

// PreserveGeneratedEnv copies previously generated secrets onto env so a
// plugin upgrade does not rotate MYSQL_PWD / MONGO_PWD out from under a
// running cluster.
func PreserveGeneratedEnv(m *Manifest, env, previous map[string]string) {
	if m == nil || previous == nil {
		return
	}
	for _, key := range m.GenerateEnv {
		if v := previous[key]; v != "" {
			env[key] = v
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

// ClusterEnv reads well-known secrets from already-running core apps
// (controller/postgres). It does not assume any plugin is installed.
func ClusterEnv(client appReleaseGetter) (map[string]string, error) {
	out := map[string]string{}
	for _, app := range []string{"controller", "postgres"} {
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
		if v := release.Env["SINGLETON"]; v != "" {
			out["SINGLETON"] = v
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
	if out["CONTROLLER_KEY"] == "" {
		return nil, fmt.Errorf("unable to find CONTROLLER_KEY in controller or postgres release")
	}
	if out["SINGLETON"] == "" {
		out["SINGLETON"] = "false"
	}
	return out, nil
}

func SingletonWebCount(cluster map[string]string) int {
	if strings.EqualFold(cluster["SINGLETON"], "true") {
		return 1
	}
	return 2
}
