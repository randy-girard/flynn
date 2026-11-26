package plugin

import (
	"path"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

// apiAuthKeys are the plugin release env keys a plugin API accepts
// (appliancehttp.Key reads them in this order).
var apiAuthKeys = []string{"CONTROLLER_KEY", "AUTH_KEY"}

// APIAuthEnv is the plugin API key from a plugin app release, for a one-off
// task that calls that plugin's API (maintenance, scale, backups). Nothing
// else from the plugin release env is copied.
func APIAuthEnv(rel *ct.Release) map[string]string {
	out := map[string]string{}
	if rel == nil || rel.Env == nil {
		return out
	}
	for _, key := range apiAuthKeys {
		if v := strings.TrimSpace(rel.Env[key]); v != "" {
			out[key] = v
		}
	}
	return out
}

// APIBinary reports whether a job's argv runs a plugin API binary
// (/bin/flynn-<name>-api), which authenticates to its plugin API.
func APIBinary(args []string) bool {
	if len(args) == 0 {
		return false
	}
	base := path.Base(args[0])
	return strings.HasPrefix(base, "flynn-") && strings.HasSuffix(base, "-api")
}
