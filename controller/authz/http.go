package authz

import (
	"net/http"
	"strings"

	"github.com/flynn/flynn/controller/authorizer"
)

// routeKind describes how tight access must be for an HTTP request.
type routeKind int

const (
	rkCluster routeKind = iota
	rkAppRead
	rkAppWrite
	rkAppDeploy
)

// HTTPAllowed returns false if the principal may not call this controller route.
func HTTPAllowed(tok *authorizer.Token, method, rawPath string) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	kind, appID := httpRequirement(method, rawPath)
	if kind == rkCluster {
		return false
	}
	return grantCovers(tok, appID, kind)
}

func httpRequirement(method, rawPath string) (routeKind, string) {
	path := strings.Trim(rawPath, "/")
	if path == "" {
		return rkCluster, ""
	}
	parts := strings.Split(path, "/")
	m := strings.ToUpper(method)

	if len(parts) == 0 || parts[0] == "" {
		return rkCluster, ""
	}

	switch parts[0] {
	case "apps":
		if len(parts) == 1 {
			return rkCluster, ""
		}
		appID := parts[1]
		if len(parts) == 2 {
			switch m {
			case http.MethodGet, http.MethodHead:
				return rkAppRead, appID
			case http.MethodPost, http.MethodDelete:
				return rkAppWrite, appID
			default:
				return rkAppWrite, appID
			}
		}
		if m == http.MethodPost && parts[2] == "deploy" {
			return rkAppDeploy, appID
		}
		switch m {
		case http.MethodGet, http.MethodHead:
			return rkAppRead, appID
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			return rkAppWrite, appID
		default:
			return rkAppWrite, appID
		}

	default:
		return rkCluster, ""
	}
}

func grantCovers(tok *authorizer.Token, appID string, need routeKind) bool {
	perms := permissionsForApp(tok, appID)
	if len(perms) == 0 {
		return false
	}
	hasStar := false
	hasRead := false
	hasWrite := false
	hasDeploy := false
	for _, p := range perms {
		switch p {
		case "*":
			hasStar = true
		case "cluster:admin":
			hasStar = true
		case "app:read":
			hasRead = true
		case "app:write":
			hasWrite = true
		case "app:deploy":
			hasDeploy = true
		}
	}
	if hasStar {
		return true
	}
	switch need {
	case rkAppRead:
		return hasRead || hasWrite || hasDeploy
	case rkAppWrite:
		return hasWrite || hasDeploy
	case rkAppDeploy:
		return hasDeploy || hasWrite
	default:
		return false
	}
}

func permissionsForApp(tok *authorizer.Token, appID string) []string {
	for _, g := range tok.AppGrants {
		if g.AppID == appID {
			return g.Permissions
		}
	}
	return nil
}
