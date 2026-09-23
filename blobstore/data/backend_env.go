package data

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var configEnvPattern = regexp.MustCompile(`^BACKEND_([A-Z0-9]+)$`)

var backendParamOrder = []string{
	"backend",
	"region",
	"location",
	"endpoint",
	"insecure",
	"bucket",
	"container",
	"access_key_id",
	"secret_access_key",
	"ec2_role",
	"account_name",
	"account_key",
}

var secretBackendParams = map[string]bool{
	"secret_access_key": true,
	"account_key":       true,
	"key":               true,
}

// BackendEnvKey is BACKEND_<NAME> for a configured extra backend.
func BackendEnvKey(name string) string {
	return "BACKEND_" + strings.ToUpper(name)
}

// BackendOverlayEnvKey is BACKEND_<NAME>_<PARAM> (GCS JSON key, extra creds).
func BackendOverlayEnvKey(name, param string) string {
	return BackendEnvKey(name) + "_" + strings.ToUpper(param)
}

// BackendsFromEnv parses DEFAULT_BACKEND and BACKEND_* the same way
// NewFileRepoFromEnv does, without constructing backend clients.
func BackendsFromEnv(environment []string) (defaultName string, backends map[string]map[string]string, err error) {
	backends = map[string]map[string]string{
		"postgres": {"backend": "postgres"},
	}
	for _, env := range environment {
		nameInfo := strings.SplitN(env, "=", 2)
		if len(nameInfo) < 2 {
			continue
		}
		nameMatch := configEnvPattern.FindStringSubmatch(nameInfo[0])
		if len(nameMatch) < 2 {
			continue
		}
		name := strings.ToLower(nameMatch[1])
		info, err := parseBackendInfo(environment, name, nameInfo[1])
		if err != nil {
			return "", nil, err
		}
		backends[name] = info
	}
	defaultName = "postgres"
	for _, env := range environment {
		if !strings.HasPrefix(env, "DEFAULT_BACKEND=") {
			continue
		}
		if d := strings.TrimPrefix(env, "DEFAULT_BACKEND="); d != "" {
			defaultName = d
		}
		break
	}
	return defaultName, backends, nil
}

func parseBackendInfo(environment []string, name, params string) (map[string]string, error) {
	info := make(map[string]string)
	for _, token := range strings.Split(params, " ") {
		if token == "" {
			continue
		}
		kv := strings.SplitN(token, "=", 2)
		if len(kv) < 2 {
			return nil, fmt.Errorf("blobstore: error parsing backend kv pair %q", token)
		}
		info[kv[0]] = kv[1]
	}
	prefix := strings.ToUpper(fmt.Sprintf("BACKEND_%s_", name))
	for _, env := range environment {
		if !strings.HasPrefix(env, prefix) {
			continue
		}
		kv := strings.SplitN(env, "=", 2)
		k := strings.ToLower(strings.TrimPrefix(kv[0], prefix))
		if len(kv) < 2 {
			info[k] = ""
			continue
		}
		info[k] = kv[1]
	}
	return info, nil
}

// EncodeBackendParams writes space-separated k=v tokens in a stable order.
func EncodeBackendParams(info map[string]string) string {
	seen := make(map[string]bool, len(backendParamOrder))
	parts := make([]string, 0, len(info))
	for _, k := range backendParamOrder {
		v := info[k]
		if v == "" {
			continue
		}
		seen[k] = true
		parts = append(parts, k+"="+v)
	}
	extra := make([]string, 0)
	for k, v := range info {
		if v == "" || seen[k] {
			continue
		}
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		parts = append(parts, k+"="+info[k])
	}
	return strings.Join(parts, " ")
}

// RedactBackendInfo copies info with secret values replaced.
func RedactBackendInfo(info map[string]string) map[string]string {
	out := make(map[string]string, len(info))
	for k, v := range info {
		if secretBackendParams[k] && v != "" {
			out[k] = "***"
			continue
		}
		out[k] = v
	}
	return out
}

// EnvMapToSlice converts a controller release env map to os.Environ form.
func EnvMapToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
