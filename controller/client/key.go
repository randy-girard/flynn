package controller

import (
	"os"
	"strings"
)

// KeyFromEnvOrMeta returns the controller API key. System jobs should have
// CONTROLLER_KEY (or AUTH_KEY, when that variable is the cluster key) in
// their environment. Instance Meta["AUTH_KEY"] is only used when the env is
// empty so mixed-version rolling updates do not deadlock after the controller
// stops publishing the key (SEC-028).
//
// CONTROLLER_KEY is preferred because some system apps (status) use AUTH_KEY
// for their own HTTP API, which is not the cluster controller key.
func KeyFromEnvOrMeta(meta map[string]string) string {
	if k := strings.TrimSpace(os.Getenv("CONTROLLER_KEY")); k != "" {
		return k
	}
	if k := strings.TrimSpace(os.Getenv("AUTH_KEY")); k != "" {
		return k
	}
	if meta != nil {
		return strings.TrimSpace(meta["AUTH_KEY"])
	}
	return ""
}
