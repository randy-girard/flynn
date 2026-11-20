package authz

import (
	"net/http"
	"sort"
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
	// HTTPAllowed only checks that the caller can mint a release for some
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

// ClusterRegionPublic is the unauthenticated peer read. The body is only
// this cluster's region name. Linking does not send CONTROLLER_KEY.
func ClusterRegionPublic(method, path string) bool {
	return method == http.MethodGet && path == "/cluster/region"
}

// HTTPAllowed returns false if the principal may not call this controller route.
func HTTPAllowed(tok *authorizer.Token, method, rawPath string) bool {
	return HTTPAllowedResolved(tok, method, rawPath, nil)
}

// HTTPAllowedResolved is HTTPAllowed with a resolver that turns the app
// reference in the path (a name or an id) into the app id. Grants are keyed by
// id, so the check runs on the resolved id. A nil resolver, or one that returns
// "", checks the path reference as given.
func HTTPAllowedResolved(tok *authorizer.Token, method, rawPath string, resolve func(ref string) string) bool {
	if tok == nil {
		return false
	}
	kind, appID, perm := httpRequirement(method, rawPath)
	// grantID is the app id the grants are checked against. It is resolved
	// lazily so routes that never reach a grant check cost no lookup.
	grantID := func() string {
		if resolve != nil && appID != "" {
			if id := resolve(appID); id != "" {
				return id
			}
		}
		return appID
	}
	// Checked before the cluster-admin short-circuit so a cluster:admin JWT
	// cannot create compiles. The cluster key still can.
	if kind == rkGitreceiveBuild {
		if tok.ClusterKey {
			return true
		}
		return hasScope(tok, ScopeGitreceiveBuild) && grantCovers(tok, grantID(), PermAppWrite)
	}
	if tok.HasClusterAdmin() {
		return true
	}
	// flynn-receiver reads the buildpack allowlist before it starts a
	// compile. Read-only, and only for the receive credential.
	if hasScope(tok, ScopeGitreceiveBuild) && (method == http.MethodGet || method == http.MethodHead) && strings.Trim(rawPath, "/") == "cluster/policy" {
		return true
	}
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
	// Service tokens reach only their own routes. This runs before the
	// user-session and provider checks so a caller label in UserID cannot
	// open them.
	if IsServiceCredential(tok) && (kind == rkUserSession || kind == rkManageProvider) {
		return (hasScope(tok, ScopeAccountQuota) && accountQuotaRead(method, rawPath)) ||
			(hasScope(tok, ScopeACMEIssue) && acmeIssueAllowed(method, rawPath))
	}
	if kind == rkUserSession {
		return UserBound(tok)
	}
	if kind == rkManageProvider {
		return UserBound(tok) || tokenCanManageSomeApp(tok)
	}
	if kind == rkEvents {
		return hasScope(tok, ScopeEventsRead) && hasEventsGrant(tok)
	}
	if kind == rkArtifactGet {
		return hasScope(tok, ScopeGitreceiveBuild) && hasAnyGrantedApp(tok)
	}
	if kind == rkAccountQuota {
		return hasScope(tok, ScopeAccountQuota)
	}
	if hasScope(tok, ScopeAccountQuota) && accountQuotaRead(method, rawPath) {
		return true
	}
	if hasScope(tok, ScopeACMEIssue) && acmeIssueAllowed(method, rawPath) {
		return true
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
	return grantCovers(tok, grantID(), perm)
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
// HTTPAllowed requires for POST /apps/:id/deploy. Coarser aliases that expand
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

func accountQuotaRead(method, rawPath string) bool {
	m := strings.ToUpper(method)
	if m != http.MethodGet && m != http.MethodHead {
		return false
	}
	path := strings.Trim(rawPath, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] == "apps" {
		return true
	}
	// GET /apps/:id/formations lists process counts per release. It carries
	// no release env; single formations are not opened because
	// ?expand=true embeds the release.
	return len(parts) == 3 && parts[0] == "apps" && parts[1] != "" && parts[2] == "formations"
}

// HasAccountQuota reports whether tok is the billing quota credential.
func HasAccountQuota(tok *authorizer.Token) bool {
	return hasScope(tok, ScopeAccountQuota)
}

// HasACMEIssue reports whether tok is the ACME issuance credential.
func HasACMEIssue(tok *authorizer.Token) bool {
	return hasScope(tok, ScopeACMEIssue)
}

func acmeIssueAllowed(method, rawPath string) bool {
	m := strings.ToUpper(method)
	parts := strings.Split(strings.Trim(rawPath, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	switch parts[0] {
	case "managed-certificates":
		if len(parts) == 1 {
			return m == http.MethodGet || m == http.MethodHead
		}
		return len(parts) == 2 && (m == http.MethodGet || m == http.MethodHead || m == http.MethodPut)
	case "acme":
		return len(parts) == 2 && parts[1] == "config" && (m == http.MethodGet || m == http.MethodHead)
	case "routes":
		return len(parts) == 1 && (m == http.MethodGet || m == http.MethodHead)
	case "apps":
		if len(parts) == 1 && (m == http.MethodGet || m == http.MethodHead) {
			return true
		}
		if len(parts) >= 3 && parts[2] == "routes" {
			switch m {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				return true
			}
		}
	}
	return false
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
	case "events":
		if m == http.MethodGet || m == http.MethodHead {
			return rkEvents, "", ""
		}
		return rkCluster, "", ""
	case "tenancy":
		if m == http.MethodGet || m == http.MethodHead {
			return rkAnyAuth, "", ""
		}
		return rkCluster, "", ""
	case "whoami", "tokens", "domains":
		return rkUserSession, "", ""
	case "users":
		// POST /users creates a pre-verified account for any email. Only a
		// cluster admin may do that: a user session could squat an address
		// and receive the grants made when its owner is later invited.
		return rkCluster, "", ""
	case "handles":
		if m == http.MethodGet || m == http.MethodHead {
			return rkUserSession, "", ""
		}
		return rkCluster, "", ""
	case "accounts":
		if len(parts) >= 3 && parts[2] == "collaborators" {
			return rkUserSession, "", ""
		}
		if len(parts) == 3 && parts[2] == "quota" && (m == http.MethodGet || m == http.MethodHead || m == http.MethodPut) {
			return rkAccountQuota, "", ""
		}
		if len(parts) == 3 && (parts[2] == "suspend" || parts[2] == "unsuspend") && m == http.MethodPost {
			return rkAccountQuota, "", ""
		}
		return rkCluster, "", ""
	case "providers":
		if len(parts) == 4 && parts[2] == "resources" {
			// One resource carries its credentials. Reading it is for a user
			// or an app grant (the handler checks the owner). Writing it is
			// bootstrap, migration and plugin controller keys only.
			switch m {
			case http.MethodGet, http.MethodHead:
				return rkManageProvider, "", ""
			case http.MethodPut:
				return rkCluster, "", ""
			}
		}
		if m == http.MethodGet || m == http.MethodHead {
			return rkAnyAuth, "", ""
		}
		if len(parts) >= 3 && parts[2] == "resources" {
			return rkManageProvider, "", ""
		}
		return rkCluster, "", ""
	case "routes":
		if m == http.MethodGet || m == http.MethodHead {
			return rkUserSession, "", ""
		}
		return rkCluster, "", ""
	case "hosts":
		if m == http.MethodGet || m == http.MethodHead {
			return rkAnyAuth, "", ""
		}
		return rkCluster, "", ""
	case "runtimes":
		if m == http.MethodGet || m == http.MethodHead {
			return rkAnyAuth, "", ""
		}
		return rkCluster, "", ""
	case "db-runtimes":
		// List is for anyone who can provision. Creates are flynn-host
		// (cluster key, including plugin jobs) or a cluster admin on the web.
		if m == http.MethodGet || m == http.MethodHead {
			return rkAnyAuth, "", ""
		}
		return rkCluster, "", ""
	case "cluster":
		// GET /cluster/regions is the list the dashboard and CLI show. Linking
		// stays cluster-admin. GET /cluster/region (singular) is public and
		// is served before this check.
		if len(parts) == 2 && parts[1] == "regions" && (m == http.MethodGet || m == http.MethodHead) {
			return rkAnyAuth, "", ""
		}
		if len(parts) >= 2 && (parts[1] == "runtime-settings" || parts[1] == "app-domain" || parts[1] == "placement" || parts[1] == "postgres-availability" || parts[1] == "mysql-availability" || parts[1] == "redis-availability" || parts[1] == "mongodb-availability" || parts[1] == "kafka-availability" || parts[1] == "clickhouse-availability") {
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
		if len(parts) >= 2 && parts[1] == "export" {
			return rkCluster, "", ""
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
		if (m == http.MethodGet || m == http.MethodHead) && len(parts) == 2 && parts[1] != "" {
			return rkArtifactGet, "", ""
		}
		return rkCluster, "", ""
	case "tenant-backups":
		return rkCluster, "", ""
	case "resources":
		// GET /resources is every resource the caller may see. The handler
		// keeps a user's list to the resources its accounts own (flynn
		// resource and plugin commands find a resource by name here). Only a
		// user session may ask; app-scoped and compile tokens use
		// /apps/:id/resources.
		if len(parts) == 1 && (m == http.MethodGet || m == http.MethodHead) {
			return rkUserSession, "", ""
		}
		return rkCluster, "", ""
	case "plugin-catalog":
		// GET /plugin-catalog is the plugin commands and add-on types a
		// user may use (no secrets). The handler hides providers a hosted
		// tenant cannot provision.
		if len(parts) == 1 && (m == http.MethodGet || m == http.MethodHead) {
			return rkUserSession, "", ""
		}
		return rkCluster, "", ""
	case "releases":
		if m == http.MethodPost {
			return rkCreateRelease, "", ""
		}
		return rkCluster, "", ""

	case "apps":
		if len(parts) == 1 {
			if m == http.MethodGet || m == http.MethodHead || m == http.MethodPost {
				return rkUserSession, "", ""
			}
			return rkCluster, "", ""
		}
		appID := parts[1]
		// POST /apps/:id/builds is the cluster key or a gitreceive:build
		// token for that app. Spec, token mint, and other methods stay
		// cluster-only. The handler also rejects a cluster:admin JWT.
		if len(parts) >= 3 && parts[2] == "builds" {
			if m == http.MethodPost && len(parts) == 3 {
				return rkGitreceiveBuild, appID, ""
			}
			return rkCluster, "", ""
		}
		// PUT/PATCH /apps/:id/jobs/:job is the scheduler's job-state upsert,
		// not a tenant operation. A tenant's app:jobs:run grant may start
		// jobs (POST) but must not forge job rows.
		if len(parts) >= 3 && parts[2] == "jobs" && (m == http.MethodPut || m == http.MethodPatch) {
			return rkCluster, "", ""
		}
		if len(parts) >= 3 && (parts[2] == "collaborators" || parts[2] == "transfer") {
			return rkUserSession, appID, ""
		}
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
		if len(rest) >= 3 && rest[2] == "output" && (method == http.MethodGet || method == http.MethodHead) {
			return PermAppLogsRead
		}
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
		if len(rest) >= 3 && rest[2] == "output" {
			if write {
				return PermAppWrite
			}
			return PermAppLogsRead
		}
		if write {
			return PermAppWrite
		}
		return PermAppActivityRead
	case "resources", "plugin-addons":
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
