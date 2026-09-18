package router

import "strings"

// HTTPPathRequiresClusterAdmin reports whether an HTTP route path is a
// non-root path (example.com/admin). Those routes are restricted to
// flynn-host / cluster-admin callers so app operators cannot publish
// path-based apps from the Flynn CLI or dashboard.
func HTTPPathRequiresClusterAdmin(path string) bool {
	p := strings.TrimSpace(path)
	return p != "" && p != "/"
}
