package data

import "github.com/randy-girard/flynn/blobstore/backendenv"

// The parsers live in blobstore/backendenv, which has no backend SDK
// dependency. These names stay for the blobstore and its callers.

var configEnvPattern = backendenv.ConfigEnvPattern

// BackendEnvKey is BACKEND_<NAME> for a configured extra backend.
func BackendEnvKey(name string) string { return backendenv.BackendEnvKey(name) }

// BackendOverlayEnvKey is BACKEND_<NAME>_<PARAM> (GCS JSON key, extra creds).
func BackendOverlayEnvKey(name, param string) string {
	return backendenv.BackendOverlayEnvKey(name, param)
}

// BackendsFromEnv parses DEFAULT_BACKEND and BACKEND_* the same way
// NewFileRepoFromEnv does, without constructing backend clients.
func BackendsFromEnv(environment []string) (string, map[string]map[string]string, error) {
	return backendenv.BackendsFromEnv(environment)
}

func parseBackendInfo(environment []string, name, params string) (map[string]string, error) {
	return backendenv.ParseBackendInfo(environment, name, params)
}

// EncodeBackendParams writes space-separated k=v tokens in a stable order.
func EncodeBackendParams(info map[string]string) string {
	return backendenv.EncodeBackendParams(info)
}

// RedactBackendInfo copies info with secret values replaced.
func RedactBackendInfo(info map[string]string) map[string]string {
	return backendenv.RedactBackendInfo(info)
}

// EnvMapToSlice converts a controller release env map to os.Environ form.
func EnvMapToSlice(env map[string]string) []string { return backendenv.EnvMapToSlice(env) }
