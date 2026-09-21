package authz

import (
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
)

// routeKind describes how tight access must be for an HTTP request.
type routeKind int

const (
	rkCluster routeKind = iota
	rkAppAccess
	// rkBuildArtifact is the cluster-level artifact-creation route
	// (POST /artifacts). Artifacts are global (not app-scoped in the URL),
	// so this cannot be a per-app grant; it is instead gated on the
	// build:artifacts scope, which the gitreceive receiver mints only
	// alongside an app grant for the app being built.
	rkBuildArtifact
	// rkAnyAuth is any valid controller credential (cluster admin or
	// app-scoped). Used for read-only cluster catalogs such as runtime
	// profiles that app operators must list in order to apply them.
	rkAnyAuth
	// rkCreateRelease is POST /releases. The app id is in the body, so
	// HTTPAllowed only checks that the caller can write some app (or is a
	// cluster admin). CreateRelease enforces the specific app.
	rkCreateRelease
	// rkGitHubCatalog is GET/HEAD /github/installations and
	// /github/installations/:id/repos. Cluster admins see every GitHub App
	// installation. App-scoped tokens need github:write on at least one app;
	// the handler then filters to installations linked to those apps.
	rkGitHubCatalog
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
	kind, appID, perm := httpRequirement(method, rawPath)
	if kind == rkBuildArtifact {
		return hasScopedBuildArtifact(tok)
	}
	if kind == rkAnyAuth {
		return true
	}
	if kind == rkCreateRelease {
		return hasAnyReleaseWrite(tok)
	}
	if kind == rkGitHubCatalog {
		return hasAnyGitHubWrite(tok)
	}
	if kind == rkCluster {
		return false
	}
	// Platform apps hold cluster state (controller/blobstore/postgres, …).
	// App-scoped dashboard tokens must not open their consoles or read env
	// even if someone granted the token that app id/name.
	if IsPlatformAppName(appID) {
		return false
	}
	return grantCovers(tok, appID, perm)
}

// TokenContextKey stores the request principal on the handler context.
type tokenContextKey struct{}

// TokenContextKey is the context key for *authorizer.Token.
var TokenContextKey = tokenContextKey{}

// TokenFromContext returns the principal muxHandler stored, or nil.
func TokenFromContext(ctx interface {
	Value(key interface{}) interface{}
}) *authorizer.Token {
	if ctx == nil {
		return nil
	}
	tok, _ := ctx.Value(TokenContextKey).(*authorizer.Token)
	return tok
}

// SystemAppAllowed reports whether tok may operate on a flynn-system-app.
// User apps are always allowed at this layer (HTTPAllowed already scoped them).
// A nil token cannot touch system apps.
func SystemAppAllowed(tok *authorizer.Token, systemApp bool) bool {
	if !systemApp {
		return true
	}
	return tok != nil && tok.HasClusterAdmin()
}

// HideInternalProcesses is true when a user-app API response must omit
// git-deploy internals (slugbuilder/dockerbuilder/slugrunner) from jobs,
// logs, formations, metrics, and release process maps. The cluster
// controller key still sees them. Dashboard JWTs do not; operators inspect
// those jobs with flynn-host and set builder limits with `flynn limit:set`.
func HideInternalProcesses(tok *authorizer.Token, systemApp bool) bool {
	if systemApp {
		return false
	}
	if tok == nil || tok.ClusterKey {
		return false
	}
	return true
}

// CanManageInternalProcessLimits reports whether tok may see and change
// slugbuilder/dockerbuilder/slugrunner resource limits on a user app.
// Only the cluster controller key (flynn CLI / flynn-host / gitreceive) may;
// dashboard JWTs, including cluster:admin and app:admin, may not.
func CanManageInternalProcessLimits(tok *authorizer.Token, appID string) bool {
	return tok == nil || tok.ClusterKey
}

// platformAppNames are bootstrap/system apps addressed by name in the URL.
// UUID lookups are enforced in appLookup via App.System().
var platformAppNames = map[string]struct{}{
	"blobstore":     {},
	"controller":    {},
	"dashboard":     {},
	"discoverd":     {},
	"flannel":       {},
	"gitreceive":    {},
	"logaggregator": {},
	"postgres":      {},
	"router":        {},
	"status":        {},
	"tarreceive":    {},
}

// IsPlatformAppName is true for well-known system app names (not redis-<uuid>
// appliances). Those appliances are still flynn-system-app and are gated in
// appLookup with SystemAppAllowed.
func IsPlatformAppName(name string) bool {
	_, ok := platformAppNames[name]
	return ok
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

func httpRequirement(method, rawPath string) (kind routeKind, appID, perm string) {
	path := strings.Trim(rawPath, "/")
	if path == "" {
		return rkCluster, "", ""
	}
	parts := strings.Split(path, "/")
	m := strings.ToUpper(method)

	if len(parts) == 0 || parts[0] == "" {
		return rkCluster, "", ""
	}

	switch parts[0] {
	case "runtime-profiles":
		if m == http.MethodGet || m == http.MethodHead {
			return rkAnyAuth, "", ""
		}
		return rkCluster, "", ""
	case "cluster":
		if len(parts) >= 2 && parts[1] == "runtime-settings" {
			if m == http.MethodGet || m == http.MethodHead {
				return rkAnyAuth, "", ""
			}
			return rkCluster, "", ""
		}
		return rkCluster, "", ""
	case "github":
		if len(parts) >= 2 && parts[1] == "webhook" {
			return rkAnyAuth, "", ""
		}
		if m == http.MethodGet || m == http.MethodHead {
			if len(parts) >= 2 && parts[1] == "installations" {
				return rkGitHubCatalog, "", ""
			}
			// GET /github/app stays any-auth so the dashboard can show
			// "GitHub App is not configured" vs the connect panel.
			return rkAnyAuth, "", ""
		}
		return rkCluster, "", ""
	case "artifacts":
		// POST /artifacts is the build artifact-creation route. Only the
		// method matters; the artifact is not app-scoped in the URL.
		if m == http.MethodPost {
			return rkBuildArtifact, "", ""
		}
		return rkCluster, "", ""
	case "releases":
		if m == http.MethodPost {
			return rkCreateRelease, "", ""
		}
		return rkCluster, "", ""

	case "apps":
		if len(parts) == 1 {
			return rkCluster, "", ""
		}
		appID := parts[1]
		if len(parts) == 2 {
			switch m {
			case http.MethodGet, http.MethodHead:
				return rkAppAccess, appID, ""
			case http.MethodDelete:
				return rkAppAccess, appID, PermAppDelete
			default:
				return rkAppAccess, appID, PermAppWrite
			}
		}
		return rkAppAccess, appID, appSubPerm(m, parts[2:])

	default:
		return rkCluster, "", ""
	}
}

func appSubPerm(method string, rest []string) string {
	if len(rest) == 0 {
		return PermAppRead
	}
	res := rest[0]
	write := method != http.MethodGet && method != http.MethodHead
	switch res {
	case "log":
		return PermAppLogsRead
	case "formations", "scale":
		if write {
			return PermAppScaleWrite
		}
		return PermAppScaleRead
	case "jobs":
		switch method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			return PermAppJobsRun
		case http.MethodDelete:
			return PermAppJobsStop
		default:
			return PermAppJobsRead
		}
	case "jobs-stats":
		return PermAppMetricsRead
	case "deploy":
		return PermAppDeploy
	case "deployments":
		return PermAppActivityRead
	case "release":
		if write {
			return permSharedReleaseWrite
		}
		return ""
	case "releases":
		if write {
			return PermAppWrite
		}
		return PermAppActivityRead
	case "resources":
		if write {
			return PermAppResourcesWrite
		}
		return PermAppResourcesRead
	case "routes":
		if write {
			return PermAppRoutesWrite
		}
		return PermAppRoutesRead
	case "github":
		if len(rest) >= 2 && rest[1] == "deploy" {
			return PermAppDeploy
		}
		if write {
			return PermAppGitHubWrite
		}
		return PermAppGitHubRead
	case "volumes":
		if write {
			return PermAppResourcesWrite
		}
		return PermAppResourcesRead
	case "gc":
		return PermAppDelete
	case "meta":
		return PermAppWrite
	case "scheduler-events":
		if write {
			return PermAppSchedulerWrite
		}
		return PermAppSchedulerRead
	default:
		if write {
			return PermAppWrite
		}
		return PermAppRead
	}
}

func hasAnyReleaseWrite(tok *authorizer.Token) bool {
	for _, g := range tok.AppGrants {
		if CanCreateRelease(g.Permissions) {
			return true
		}
	}
	return false
}

func hasAnyGitHubWrite(tok *authorizer.Token) bool {
	for _, g := range tok.AppGrants {
		if HasAppPermission(g.Permissions, PermAppGitHubWrite) {
			return true
		}
	}
	return false
}

// GitHubWriteAppIDs returns app IDs the token may connect GitHub on.
// restricted is false for cluster admins (no catalog filter).
func GitHubWriteAppIDs(tok *authorizer.Token) (ids []string, restricted bool) {
	if tok == nil || tok.HasClusterAdmin() {
		return nil, false
	}
	for _, g := range tok.AppGrants {
		if HasAppPermission(g.Permissions, PermAppGitHubWrite) {
			ids = append(ids, g.AppID)
		}
	}
	return ids, true
}

func grantCovers(tok *authorizer.Token, appID, need string) bool {
	return HasAppPermission(permissionsForApp(tok, appID), need)
}

func permissionsForApp(tok *authorizer.Token, appID string) []string {
	for _, g := range tok.AppGrants {
		if g.AppID == appID {
			return g.Permissions
		}
	}
	return nil
}
