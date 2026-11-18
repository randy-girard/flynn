package pluginui

import (
	"encoding/json"
	"net/http"
)

// PermResourcesWrite is the grant that covers changing a resource instance.
const PermResourcesWrite = "app:resources:write"

// manageWrites are the fine write grants the coarse Manage grant (app:write)
// implies. Team management is not among them.
var manageWrites = map[string]bool{
	"app:deploy": true, "app:scale:write": true, "app:env:write": true,
	"app:alerts:write": true, "app:jobs:run": true, "app:jobs:stop": true,
	"app:scheduler:write": true, "app:routes:write": true,
	"app:resources:write": true, "app:github:write": true, "app:delete": true,
}

// Can reports whether perms grant need. It fails closed: no permissions means
// no access. The wildcard and coarse grants mirror the dashboard's own
// expansion (cluster admins carry "*", Manage is app:write, Admin app:admin).
func Can(perms []string, need string) bool {
	if need == "" {
		return false
	}
	for _, p := range perms {
		switch p {
		case "*", "cluster:admin", "app:admin", need:
			return true
		case "app:write":
			if manageWrites[need] {
				return true
			}
		}
	}
	return false
}

// CanWrite reports whether perms allow changing a resource (app:resources:write).
func CanWrite(perms []string) bool { return Can(perms, PermResourcesWrite) }

// Forbid writes the standard 403 JSON body.
func Forbid(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "permission denied: write access required"})
}

// RequireWrite wraps next so it runs only when the request's session has write
// access. perms returns the session permissions (nil when unauthenticated);
// empty permissions are refused.
func RequireWrite(perms func(*http.Request) []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !CanWrite(perms(r)) {
			Forbid(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}
