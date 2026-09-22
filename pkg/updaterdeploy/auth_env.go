package updaterdeploy

import (
	"os"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/updater/accesstoken"
)

// ClusterSecrets are the cluster-wide credentials copied onto system-app
// releases during flynn-host update so older clusters pick up AUTH_KEY /
// CONTROLLER_KEY / DISCOVERD_AUTH_KEY without a manual env:set.
type ClusterSecrets struct {
	ControllerKey    string
	DiscoverdAuthKey string
	AccessTokenKey   string
	HostAuthKey      string
}

// SeedAccessTokenPair loads gitreceive's access-token keypair into the
// process-local cache so blobstore/controller/tarreceive deploys (which run
// before or after gitreceive) share one pair. A missing gitreceive app is a
// no-op.
func SeedAccessTokenPair(client controller.Client) error {
	if client == nil {
		return nil
	}
	rel, err := client.GetAppRelease("gitreceive")
	if err != nil || rel == nil || rel.Env == nil {
		return nil
	}
	env := cloneEnv(rel.Env)
	_, err = accesstoken.Update("gitreceive", env)
	return err
}

// LoadClusterSecrets reads controller/postgres/gitreceive/discoverd releases
// and falls back to the updater job environment (host.json-injected secrets).
func LoadClusterSecrets(client controller.Client) ClusterSecrets {
	s := ClusterSecrets{}
	if client != nil {
		for _, appName := range []string{"controller", "postgres", "gitreceive", "discoverd"} {
			rel, err := client.GetAppRelease(appName)
			if err != nil || rel == nil || rel.Env == nil {
				continue
			}
			if s.ControllerKey == "" {
				s.ControllerKey = firstNonEmpty(rel.Env["CONTROLLER_KEY"], rel.Env["AUTH_KEY"])
			}
			if s.DiscoverdAuthKey == "" {
				s.DiscoverdAuthKey = rel.Env["DISCOVERD_AUTH_KEY"]
			}
			if s.AccessTokenKey == "" {
				s.AccessTokenKey = rel.Env["ACCESS_TOKEN_KEY"]
			}
			if s.HostAuthKey == "" {
				s.HostAuthKey = rel.Env["FLYNN_HOST_AUTH_KEY"]
			}
		}
	}
	if s.ControllerKey == "" {
		s.ControllerKey = firstNonEmpty(os.Getenv("CONTROLLER_KEY"), os.Getenv("AUTH_KEY"))
	}
	if s.DiscoverdAuthKey == "" {
		s.DiscoverdAuthKey = os.Getenv("DISCOVERD_AUTH_KEY")
	}
	if s.HostAuthKey == "" {
		s.HostAuthKey = os.Getenv("FLYNN_HOST_AUTH_KEY")
	}
	if s.AccessTokenKey == "" {
		s.AccessTokenKey = accesstoken.PublicKey()
	}
	return s
}

// BackfillAppAuth copies missing cluster secrets onto a system-app release
// env. User apps are left untouched. Returns whether env changed.
func BackfillAppAuth(client controller.Client, app *ct.App, env map[string]string) (bool, error) {
	if app == nil || env == nil || !app.System() {
		return false, nil
	}
	if err := SeedAccessTokenPair(client); err != nil {
		return false, err
	}
	updated, err := accesstoken.Update(app.Name, env)
	if err != nil {
		return false, err
	}
	if EnsureReleaseAuthEnv(app.Name, env, LoadClusterSecrets(client)) {
		updated = true
	}
	return updated, nil
}

// EnsureReleaseAuthEnv fills empty auth env vars from secrets. It never
// overwrites a non-empty value. status AUTH_KEY is a distinct status-API
// secret and is not replaced with the cluster controller key.
func EnsureReleaseAuthEnv(appName string, env map[string]string, s ClusterSecrets) bool {
	if env == nil {
		return false
	}
	changed := setIfEmpty(env, "DISCOVERD_AUTH_KEY", s.DiscoverdAuthKey)
	switch appName {
	case "status":
		return EnsureApplianceControllerKey(env, s.ControllerKey) || changed
	case "controller":
		changed = setIfEmpty(env, "AUTH_KEY", s.ControllerKey) || changed
		changed = setIfEmpty(env, "ACCESS_TOKEN_KEY", s.AccessTokenKey) || changed
		changed = setIfEmpty(env, "FLYNN_HOST_AUTH_KEY", s.HostAuthKey) || changed
		return changed
	case "blobstore", "tarreceive":
		changed = setIfEmpty(env, "AUTH_KEY", s.ControllerKey) || changed
		changed = EnsureApplianceControllerKey(env, s.ControllerKey) || changed
		changed = setIfEmpty(env, "ACCESS_TOKEN_KEY", s.AccessTokenKey) || changed
		return changed
	case "router", "acme":
		changed = setIfEmpty(env, "AUTH_KEY", s.ControllerKey) || changed
		return EnsureApplianceControllerKey(env, s.ControllerKey) || changed
	default:
		// postgres, redis-* appliances, taffy, gitreceive, logaggregator, plugins
		return EnsureApplianceControllerKey(env, s.ControllerKey) || changed
	}
}

func setIfEmpty(env map[string]string, key, val string) bool {
	if env == nil || key == "" || val == "" || env[key] != "" {
		return false
	}
	env[key] = val
	return true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func cloneEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}
