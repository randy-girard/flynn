package authz

import (
	"sort"

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
	// the route check only verifies that the caller can mint a release for some
	// app (env:write, or cluster admin). CreateRelease then requires app_id
	// for non-admin tokens and enforces the specific app.
	rkCreateRelease
	// rkGitHubCatalog is GET/HEAD /github/installations and
	// /github/installations/:id/repos. Cluster admins see every GitHub App
	// installation. App-scoped tokens need github:write on at least one app;
	// the handler then filters to installations linked to those apps.
	rkGitHubCatalog
	// rkUserSession is a route a user token (UserID set) may call. The
	// handler enforces the specific account or app. Cluster admin is already
	// allowed above. App-scoped tokens without a user id stay denied, which
	// keeps GET /apps a 403 for dashboard grants that only list one app.
	// User tokens that pass this check still must not receive the full app
	// catalog: GET /apps filters to owned/collaborator apps. Cluster-admin
	// GET /apps includes flynn-plugin apps (installed via flynn-host) and
	// omits bootstrap system apps unless ?all=true.
	rkUserSession
	// rkManageProvider is resource provision, attach, and delete. A user id
	// or an app:write / app:admin grant may reach the handler, which checks
	// the target app.
	rkManageProvider
	// rkEvents is GET /events and GET /events/:id for an events:read token.
	// The handler still requires app_id and a matching app grant. Cluster
	// admin is allowed above and may subscribe with no app_id.
	rkEvents
	// rkGitreceiveBuild is POST /apps/:id/builds. The cluster key may create
	// a compile. A gitreceive:build token may create one only for an app it
	// can write. A cluster:admin JWT may not.
	rkGitreceiveBuild
	// rkArtifactGet is GET /artifacts/:id. A gitreceive:build token with any
	// app grant may fetch one artifact (slugbuilder and slug images). Listing
	// artifacts stays cluster-only.
	rkArtifactGet
	// rkAccountQuota is quota read/write and suspend. It is not cluster admin.
	rkAccountQuota
)

// ScopeBuildArtifacts is the scope that lets a non-admin token create image
// artifacts during a build. It is deliberately narrow: on its own it grants
// nothing (see hasScopedBuildArtifact), and is only useful together with an
// app grant, so a leaked build token cannot create artifacts for other apps or
// touch anything else.
const ScopeBuildArtifacts = "build:artifacts"

// ScopeEventsRead lets a dashboard process read one app's event stream.
// It is not cluster:admin and does not expand to app:env:read.
const ScopeEventsRead = "events:read"

// IsBuildCredential reports whether tok is a compile credential: the
// build:artifacts token a build job holds or the gitreceive:build token
// flynn-receiver holds. Both are bound to one app. Their UserId is only a
// rate-limit identity ("build:<owner>"), so they must never be treated as the
// owner: no account expansion, no user-session routes (PAT minting).
func IsBuildCredential(tok *authorizer.Token) bool {
	return hasScope(tok, ScopeBuildArtifacts) || hasScope(tok, ScopeGitreceiveBuild)
}

// UserBound reports whether tok speaks for a user: it has a user id and is
// not a compile credential.
func UserBound(tok *authorizer.Token) bool {
	return tok != nil && tok.UserID != "" && !IsBuildCredential(tok) && !IsServiceCredential(tok)
}

// IsServiceCredential reports whether tok is a billing quota or ACME issuance
// service token. Their UserId is only a caller label, so they never speak for
// a user and reach only their own routes.
func IsServiceCredential(tok *authorizer.Token) bool {
	return hasScope(tok, ScopeAccountQuota) || hasScope(tok, ScopeACMEIssue)
}

// ScopeGitreceiveBuild lets flynn-receiver act on the one app it is building.
// The grant is app:write for that app. The scope itself is not cluster admin.
const ScopeGitreceiveBuild = "gitreceive:build"

// ScopeAccountQuota lets the billing plugin set quota and suspension.
// It does not expand to app:env:read.
const ScopeAccountQuota = "account:quota"

// ScopeACMEIssue lets the ACME worker update managed certificates and routes.
// It does not read release env or change formations.
const ScopeACMEIssue = "acme:issue"

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
// User apps are always allowed at this layer (the route requirement already scoped them).
// A nil token cannot touch system apps.
func SystemAppAllowed(tok *authorizer.Token, systemApp bool) bool {
	if !systemApp {
		return true
	}
	return tok != nil && tok.HasClusterAdmin()
}

// HideInternalProcesses is true when a user-app API response must omit
// git-deploy internals from formations, metrics, and release process maps.
// slugrunner jobs stay hidden. slugbuilder and dockerbuilder jobs and their
// logs stay visible so the dashboard can show git-push output. The cluster
// controller key still sees everything. Builder limits stay on
// `flynn limit:set`.
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

// PlatformAppNames is the bootstrap app names hidden from a default catalog
// page unless the app is an installed plugin (flynn-plugin=true).
func PlatformAppNames() []string {
	names := make([]string, 0, len(platformAppNames))
	for name := range platformAppNames {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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

// GitPushAllowed reports whether tok may git-push (git-receive-pack) to the
// given app. Cluster admins, including the cluster controller key, may push
// anywhere. App-scoped tokens need app:deploy on that app — the same grant
// the POST /apps/:id/deploy route requires. Coarser aliases that expand
// to app:deploy (app:write, app:admin) also qualify, so flynn-receive build
// tokens (app:write on their app) can push only that app.
func GitPushAllowed(tok *authorizer.Token, appID, appName string) bool {
	return gitAppAllowed(tok, appID, appName, PermAppDeploy)
}

// GitFetchAllowed reports whether tok may git-fetch/clone (git-upload-pack)
// the given app. Cluster admins may fetch anywhere. App-scoped tokens need
// app:read (or any grant that expands to it) on that app, so a token for one
// app cannot clone another by name.
func GitFetchAllowed(tok *authorizer.Token, appID, appName string) bool {
	return gitAppAllowed(tok, appID, appName, PermAppRead)
}

// MayReadAppEnv reports whether tok may see release env and per-process env.
// A nil token is an internal caller and is allowed. Cluster-admin and the
// cluster key are allowed. app:read, app:write, app:admin, and app:env:write
// expand to app:env:read. A logs-only or scale-only grant does not.
func MayReadAppEnv(tok *authorizer.Token, appID, appName string) bool {
	if tok == nil || tok.HasClusterAdmin() {
		return true
	}
	return HasAppPermission(GrantFor(tok, appID), PermAppEnvRead)
}

func gitAppAllowed(tok *authorizer.Token, appID, appName, need string) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	return HasAppPermission(GrantFor(tok, appID), need)
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

// HasAccountQuota reports whether tok is the billing quota credential.
func HasAccountQuota(tok *authorizer.Token) bool {
	return hasScope(tok, ScopeAccountQuota)
}

// HasACMEIssue reports whether tok is the ACME issuance credential.
func HasACMEIssue(tok *authorizer.Token) bool {
	return hasScope(tok, ScopeACMEIssue)
}

func hasAnyGrantedApp(tok *authorizer.Token) bool {
	if tok == nil {
		return false
	}
	for _, g := range tok.AppGrants {
		if g.AppID != "" && len(g.Permissions) > 0 {
			return true
		}
	}
	return false
}

// GitreceiveBuildAllowed reports whether tok may POST /apps/:id/builds.
// The cluster key may. A gitreceive:build token may only for an app its
// app:write grant names. Cluster-admin JWTs may not.
func GitreceiveBuildAllowed(tok *authorizer.Token, appID, appName string) bool {
	if tok == nil {
		return false
	}
	if tok.ClusterKey {
		return true
	}
	if !hasScope(tok, ScopeGitreceiveBuild) {
		return false
	}
	return grantCovers(tok, appID, PermAppWrite)
}

func hasAnyReleaseWrite(tok *authorizer.Token) bool {
	for _, g := range tok.AppGrants {
		if CanCreateRelease(g.Permissions) {
			return true
		}
	}
	return false
}

func tokenCanManageSomeApp(tok *authorizer.Token) bool {
	for _, g := range tok.AppGrants {
		if HasAppPermission(g.Permissions, PermAppWrite) || HasAppPermission(g.Permissions, PermAppAdmin) {
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

// CanCreateReleaseForApp is true when tok may POST /releases for this app
// (by id or name). Cluster admins may create for any app; app-scoped tokens
// need env:write on that app (scale:write alone is not enough).
func CanCreateReleaseForApp(tok *authorizer.Token, appID string) bool {
	if tok == nil {
		return false
	}
	if tok.HasClusterAdmin() {
		return true
	}
	return CanCreateRelease(GrantFor(tok, appID))
}

func grantCovers(tok *authorizer.Token, appID, need string) bool {
	return HasAppPermission(GrantFor(tok, appID), need)
}

// GrantFor returns the permissions tok holds on the app with this id. It
// matches the grant's app id exactly and never by name; callers holding only a
// name resolve it to an id first. Duplicate grants for one app are unioned.
func GrantFor(tok *authorizer.Token, appID string) []string {
	if tok == nil || appID == "" {
		return nil
	}
	var out []string
	for _, g := range tok.AppGrants {
		if g.AppID == appID {
			out = append(out, g.Permissions...)
		}
	}
	return out
}
