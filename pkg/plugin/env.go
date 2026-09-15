package plugin

import (
	"fmt"
	"strings"

	controller "github.com/flynn/flynn/controller/client"
)

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
	return env
}

// ClusterEnv reads well-known secrets from already-running core apps
// (controller/postgres). It does not assume any plugin is installed.
func ClusterEnv(client controller.Client) (map[string]string, error) {
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
