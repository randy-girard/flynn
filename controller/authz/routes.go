package authz

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/randy-girard/flynn/controller/authorizer"
)

// rkPublic is a route muxHandler serves before it authenticates the caller.
const rkPublic routeKind = -1

// Requirement is who may call one controller HTTP route. Each route
// declares one where it is registered (controller/controller.go); a route
// without one cannot be registered. Build one with the constructors below.
type Requirement struct {
	declared bool
	kind     routeKind
	// perm is the app permission an rkAppAccess route needs on the app in
	// its :apps_id parameter. Empty means any grant on that app.
	perm string
	// reason says why an rkPublic route needs no credential.
	reason string
	// scopes are service-token scopes (account:quota, acme:issue,
	// gitreceive:build) that may also call this route.
	scopes []string
}

// ClusterOnly is the cluster key or a cluster:admin token.
func ClusterOnly() Requirement { return Requirement{declared: true, kind: rkCluster} }

// Public is served before authentication. reason is required.
func Public(reason string) Requirement {
	if strings.TrimSpace(reason) == "" {
		panic("authz.Public needs a reason")
	}
	return Requirement{declared: true, kind: rkPublic, reason: reason}
}

// AnyAuth is any valid controller credential: read-only cluster catalogs.
func AnyAuth() Requirement { return Requirement{declared: true, kind: rkAnyAuth} }

// UserSession is a token that speaks for a user. The handler enforces the
// account or app.
func UserSession() Requirement { return Requirement{declared: true, kind: rkUserSession} }

// App needs perm on the app named by the route's :apps_id parameter.
// Platform apps (controller, postgres, ...) stay cluster-only.
func App(perm string) Requirement {
	if perm == "" {
		panic("authz.App needs a permission; use AppAnyGrant for any grant")
	}
	return Requirement{declared: true, kind: rkAppAccess, perm: perm}
}

// AppAnyGrant needs any grant on the app in :apps_id.
func AppAnyGrant() Requirement { return Requirement{declared: true, kind: rkAppAccess} }

// AppReleaseWrite is PUT /apps/:id/release: env:write, scale:write or
// deploy on the app.
func AppReleaseWrite() Requirement {
	return Requirement{declared: true, kind: rkAppAccess, perm: permSharedReleaseWrite}
}

// GitreceiveBuild is POST /apps/:id/builds: the cluster key, or a
// gitreceive:build token with app:write on that app. Not a cluster:admin JWT.
func GitreceiveBuild() Requirement { return Requirement{declared: true, kind: rkGitreceiveBuild} }

// BuildArtifact is POST /artifacts: a build:artifacts token with an app grant.
func BuildArtifact() Requirement { return Requirement{declared: true, kind: rkBuildArtifact} }

// ArtifactGet is GET /artifacts/:id: a gitreceive:build token with a grant.
func ArtifactGet() Requirement { return Requirement{declared: true, kind: rkArtifactGet} }

// CreateRelease is POST /releases: env:write on some app. The handler
// checks the app in the body.
func CreateRelease() Requirement { return Requirement{declared: true, kind: rkCreateRelease} }

// GitHubCatalog is the GitHub installation list: github:write on some app.
// The handler filters to the caller's apps.
func GitHubCatalog() Requirement { return Requirement{declared: true, kind: rkGitHubCatalog} }

// ManageProvider is resource provision, attach and delete: a user, or
// app:write on some app. The handler checks the target app.
func ManageProvider() Requirement { return Requirement{declared: true, kind: rkManageProvider} }

// Events is the event stream: an events:read token with a grant.
func Events() Requirement { return Requirement{declared: true, kind: rkEvents} }

// AccountQuota is quota and suspension: the account:quota token.
func AccountQuota() Requirement { return Requirement{declared: true, kind: rkAccountQuota} }

// Also lets service tokens with these scopes call the route too. Only
// account:quota, acme:issue and gitreceive:build are service scopes.
func (r Requirement) Also(scopes ...string) Requirement {
	for _, s := range scopes {
		switch s {
		case ScopeAccountQuota, ScopeACMEIssue, ScopeGitreceiveBuild:
		default:
			panic(fmt.Sprintf("authz: %q is not a service scope", s))
		}
	}
	r.scopes = append(append([]string{}, r.scopes...), scopes...)
	return r
}

// Declared is false for the zero Requirement.
func (r Requirement) Declared() bool { return r.declared }

// IsPublic reports whether the route is served before authentication.
func (r Requirement) IsPublic() bool { return r.declared && r.kind == rkPublic }

// String describes the requirement, for tests and docs.
func (r Requirement) String() string {
	if !r.declared {
		return "undeclared"
	}
	var s string
	switch r.kind {
	case rkPublic:
		s = "public (" + r.reason + ")"
	case rkCluster:
		s = "cluster"
	case rkAnyAuth:
		s = "any-auth"
	case rkUserSession:
		s = "user"
	case rkAppAccess:
		switch r.perm {
		case "":
			s = "app:any-grant"
		case permSharedReleaseWrite:
			s = "app:release-write"
		default:
			s = "app " + r.perm
		}
	case rkGitreceiveBuild:
		s = "gitreceive-build"
	case rkBuildArtifact:
		s = "build-artifact"
	case rkArtifactGet:
		s = "artifact-get"
	case rkCreateRelease:
		s = "create-release"
	case rkGitHubCatalog:
		s = "github-catalog"
	case rkManageProvider:
		s = "manage-provider"
	case rkEvents:
		s = "events"
	case rkAccountQuota:
		s = "account-quota"
	}
	if len(r.scopes) > 0 {
		s += " +" + strings.Join(r.scopes, ",")
	}
	return s
}

// Allows reports whether tok may call a route with this requirement.
// appRef is the :apps_id path value ("" when the route has none); resolve
// turns it into the app id the grants are keyed by (nil or "" keeps it).
func (r Requirement) Allows(tok *authorizer.Token, appRef string, resolve func(ref string) string) bool {
	if !r.declared {
		return false
	}
	if r.kind == rkPublic {
		return true
	}
	if tok == nil {
		return false
	}
	// grantID is resolved lazily so routes that never reach a grant check
	// cost no lookup.
	grantID := func() string {
		if resolve != nil && appRef != "" {
			if id := resolve(appRef); id != "" {
				return id
			}
		}
		return appRef
	}
	// Checked before the cluster-admin short-circuit so a cluster:admin JWT
	// cannot create compiles. The cluster key still can.
	if r.kind == rkGitreceiveBuild {
		if tok.ClusterKey {
			return true
		}
		return hasScope(tok, ScopeGitreceiveBuild) && grantCovers(tok, grantID(), PermAppWrite)
	}
	if tok.HasClusterAdmin() {
		return true
	}
	switch r.kind {
	case rkBuildArtifact:
		return hasScopedBuildArtifact(tok)
	case rkAnyAuth:
		// The ACME token reaches only the rows that name its scope, not
		// the read-only cluster catalogs (NEW-30).
		if hasScope(tok, ScopeACMEIssue) {
			return r.hasScope(ScopeACMEIssue)
		}
		return true
	case rkCreateRelease:
		return hasAnyReleaseWrite(tok)
	case rkGitHubCatalog:
		return hasAnyGitHubWrite(tok)
	}
	// Service tokens reach only their own routes. This runs before the
	// user-session and provider checks so a caller label in UserID cannot
	// open them.
	if IsServiceCredential(tok) && (r.kind == rkUserSession || r.kind == rkManageProvider) {
		for _, s := range r.scopes {
			if (s == ScopeAccountQuota || s == ScopeACMEIssue) && hasScope(tok, s) {
				return true
			}
		}
		return false
	}
	switch r.kind {
	case rkUserSession:
		return UserBound(tok)
	case rkManageProvider:
		return UserBound(tok) || tokenCanManageSomeApp(tok)
	case rkEvents:
		return hasScope(tok, ScopeEventsRead) && hasEventsGrant(tok)
	case rkArtifactGet:
		return hasScope(tok, ScopeGitreceiveBuild) && hasAnyGrantedApp(tok)
	case rkAccountQuota:
		return hasScope(tok, ScopeAccountQuota)
	}
	for _, s := range r.scopes {
		if hasScope(tok, s) {
			return true
		}
	}
	if r.kind == rkCluster {
		return false
	}
	// Platform apps hold cluster state (controller/blobstore/postgres, …).
	// App-scoped dashboard tokens must not open their consoles or read env
	// even if someone granted the token that app id/name.
	if IsPlatformAppName(appRef) {
		return false
	}
	return grantCovers(tok, grantID(), r.perm)
}

// RouteTable maps controller routes (method and httprouter pattern) to
// their requirement.
type RouteTable struct {
	rows map[string][]routeRow
}

type routeRow struct {
	pattern string
	segs    []string
	req     Requirement
}

// NewRouteTable returns an empty table.
func NewRouteTable() *RouteTable { return &RouteTable{rows: map[string][]routeRow{}} }

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// Add declares req for method and pattern. It panics on an undeclared
// requirement or a duplicate row, so a route cannot be registered without
// one.
func (t *RouteTable) Add(method, pattern string, req Requirement) {
	if !req.declared {
		panic(fmt.Sprintf("authz: %s %s has no requirement", method, pattern))
	}
	for _, row := range t.rows[method] {
		if row.pattern == pattern {
			panic(fmt.Sprintf("authz: %s %s declared twice", method, pattern))
		}
	}
	t.rows[method] = append(t.rows[method], routeRow{pattern: pattern, segs: splitPath(pattern), req: req})
}

// Lookup returns the requirement of the route that serves method and path,
// and the :apps_id value. HEAD uses the GET row. ok is false when no route
// matches.
func (t *RouteTable) Lookup(method, path string) (req Requirement, appRef string, ok bool) {
	row, appRef := t.match(method, path)
	if row == nil {
		return Requirement{}, "", false
	}
	return row.req, appRef, true
}

// Pattern is the pattern of the row that serves method and path, or "".
func (t *RouteTable) Pattern(method, path string) string {
	row, _ := t.match(method, path)
	if row == nil {
		return ""
	}
	return row.pattern
}

func (t *RouteTable) match(method, path string) (*routeRow, string) {
	m := strings.ToUpper(method)
	parts := splitPath(path)
	rows := t.rows[m]
	if m == http.MethodHead {
		rows = t.rows[http.MethodGet]
	}
	var best *routeRow
	bestStatic := -1
	for i := range rows {
		row := &rows[i]
		if len(row.segs) != len(parts) {
			continue
		}
		static, match := 0, true
		for j, seg := range row.segs {
			if strings.HasPrefix(seg, ":") {
				continue
			}
			if seg != parts[j] {
				match = false
				break
			}
			static++
		}
		// httprouter prefers a static segment over a parameter.
		if match && static > bestStatic {
			best, bestStatic = row, static
		}
	}
	if best == nil {
		return nil, ""
	}
	appRef := ""
	for j, seg := range best.segs {
		if seg == ":apps_id" {
			appRef = parts[j]
		}
	}
	return best, appRef
}

// Allowed reports whether tok may call method and path. A request no route
// serves is cluster-only: the router answers 404 to the cluster key and
// cluster admins, and 403 to everyone else.
func (t *RouteTable) Allowed(tok *authorizer.Token, method, path string, resolve func(ref string) string) bool {
	req, appRef, ok := t.Lookup(method, path)
	if !ok {
		req = ClusterOnly()
	}
	return req.Allows(tok, appRef, resolve)
}

// Public reports whether method and path are served before authentication.
func (t *RouteTable) Public(method, path string) bool {
	req, _, ok := t.Lookup(method, path)
	return ok && req.IsPublic()
}

// Each calls fn for every row, in registration order per method.
func (t *RouteTable) Each(fn func(method, pattern string, req Requirement)) {
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, row := range t.rows[m] {
			fn(m, row.pattern, row.req)
		}
	}
	for m, rows := range t.rows {
		switch m {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			continue
		}
		for _, row := range rows {
			fn(m, row.pattern, row.req)
		}
	}
}

func (r Requirement) hasScope(scope string) bool {
	for _, s := range r.scopes {
		if s == scope {
			return true
		}
	}
	return false
}
