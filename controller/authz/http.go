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
	// rkBuildArtifact is the cluster-level artifact-creation route
	// (POST /artifacts). Artifacts are global (not app-scoped in the URL),
	// so this cannot be a per-app grant; it is instead gated on the
	// build:artifacts scope, which the gitreceive receiver mints only
	// alongside an app grant for the app being built.
	rkBuildArtifact
)

// ScopeBuildArtifacts is the scope that lets a non-admin token create image
// artifacts during a build. It is deliberately narrow: on its own it grants
// nothing (see hasScopedBuildArtifact), and is only useful together with an
// app grant, so a leaked build token cannot create artifacts for other apps or
// touch anything else.
const ScopeBuildArtifacts = "build:artifacts"

// HTTPAllowed returns false if the principal may not call this controller route.
func HTTPAllowed(tok *authorizer.Token, method, rawPath string) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	kind, appID := httpRequirement(method, rawPath)
	if kind == rkBuildArtifact {
		return hasScopedBuildArtifact(tok)
	}
	if kind == rkCluster {
		return false
	}
	return grantCovers(tok, appID, kind)
}

// TarreceiveAllowed reports whether a token may push layers/artifacts to
// tarreceive. tarreceive proxies uploads using its own cluster key, so callers
// must be either a cluster admin or an authorized builder (build:artifacts
// scope plus an app grant).
func TarreceiveAllowed(tok *authorizer.Token) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	return hasScopedBuildArtifact(tok)
}

// hasScopedBuildArtifact reports whether the token may create a build artifact:
// it must carry the build:artifacts scope AND at least one app grant, so the
// credential is always tied to a specific app being built and is worthless on
// its own.
func hasScopedBuildArtifact(tok *authorizer.Token) bool {
	if len(tok.AppGrants) == 0 {
		return false
	}
	for _, s := range tok.Scopes {
		if s == ScopeBuildArtifacts {
			return true
		}
	}
	return false
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
	case "artifacts":
		// POST /artifacts is the build artifact-creation route. Only the
		// method matters; the artifact is not app-scoped in the URL.
		if m == http.MethodPost {
			return rkBuildArtifact, ""
		}
		return rkCluster, ""

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
