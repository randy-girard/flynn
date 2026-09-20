package authz

// Fine-grained app grants (function + action). Coarse aliases app:read,
// app:deploy, app:write, and app:admin still exist and expand to these.
const (
	PermAppOverviewRead   = "app:overview:read"
	PermAppScaleRead      = "app:scale:read"
	PermAppScaleWrite     = "app:scale:write"
	PermAppEnvRead        = "app:env:read"
	PermAppEnvWrite       = "app:env:write"
	PermAppLogsRead       = "app:logs:read"
	PermAppMetricsRead    = "app:metrics:read"
	PermAppAlertsRead     = "app:alerts:read"
	PermAppAlertsWrite    = "app:alerts:write"
	PermAppJobsRead       = "app:jobs:read"
	PermAppJobsRun        = "app:jobs:run"
	PermAppJobsStop       = "app:jobs:stop"
	PermAppSchedulerRead  = "app:scheduler:read"
	PermAppSchedulerWrite = "app:scheduler:write"
	PermAppActivityRead   = "app:activity:read"
	PermAppRoutesRead     = "app:routes:read"
	PermAppRoutesWrite    = "app:routes:write"
	PermAppResourcesRead  = "app:resources:read"
	PermAppResourcesWrite = "app:resources:write"
	PermAppGitHubRead     = "app:github:read"
	PermAppGitHubWrite    = "app:github:write"
	PermAppTeamRead       = "app:team:read"
	PermAppTeamWrite      = "app:team:write"
	PermAppDelete         = "app:delete"
)

// permSharedReleaseWrite is an HTTP-only sentinel: PUT /apps/:id/release is
// used by env edits, scale limits, and deploys.
const permSharedReleaseWrite = "app:release:write"

var appReadPerms = []string{
	PermAppOverviewRead,
	PermAppScaleRead,
	PermAppEnvRead,
	PermAppLogsRead,
	PermAppMetricsRead,
	PermAppAlertsRead,
	PermAppJobsRead,
	PermAppSchedulerRead,
	PermAppActivityRead,
	PermAppRoutesRead,
	PermAppResourcesRead,
	PermAppGitHubRead,
	PermAppTeamRead,
}

var appManageWritePerms = []string{
	PermAppDeploy,
	PermAppScaleWrite,
	PermAppEnvWrite,
	PermAppAlertsWrite,
	PermAppJobsRun,
	PermAppJobsStop,
	PermAppSchedulerWrite,
	PermAppRoutesWrite,
	PermAppResourcesWrite,
	PermAppGitHubWrite,
	PermAppDelete,
}

var appFinePermissions []string

func init() {
	appFinePermissions = append(append([]string{}, appReadPerms...), appManageWritePerms...)
	appFinePermissions = append(appFinePermissions, PermAppTeamWrite)
}

func writeImpliesRead(p string) []string {
	switch p {
	case PermAppScaleWrite:
		return []string{PermAppScaleRead}
	case PermAppEnvWrite:
		return []string{PermAppEnvRead}
	case PermAppAlertsWrite:
		return []string{PermAppAlertsRead}
	case PermAppJobsRun, PermAppJobsStop:
		return []string{PermAppJobsRead}
	case PermAppSchedulerWrite:
		return []string{PermAppSchedulerRead}
	case PermAppRoutesWrite:
		return []string{PermAppRoutesRead}
	case PermAppResourcesWrite:
		return []string{PermAppResourcesRead}
	case PermAppGitHubWrite:
		return []string{PermAppGitHubRead}
	case PermAppTeamWrite:
		return []string{PermAppTeamRead}
	default:
		return nil
	}
}

func markAll(out map[string]bool, ids []string) {
	for _, id := range ids {
		out[id] = true
	}
}

func hasAll(out map[string]bool, ids []string) bool {
	for _, id := range ids {
		if !out[id] {
			return false
		}
	}
	return true
}

// ExpandAppPermissions turns stored grants (coarse aliases and/or fine
// function actions) into the implied set of fine permissions.
func ExpandAppPermissions(perms []string) map[string]bool {
	out := make(map[string]bool, len(appFinePermissions))
	for _, p := range perms {
		switch p {
		case "*", "cluster:admin", PermAppAdmin:
			markAll(out, appFinePermissions)
			return out
		case PermAppWrite:
			markAll(out, appReadPerms)
			markAll(out, appManageWritePerms)
		case PermAppDeploy:
			out[PermAppDeploy] = true
			markAll(out, appReadPerms)
		case PermAppRead:
			markAll(out, appReadPerms)
		default:
			if !KnownAppPermission(p) {
				continue
			}
			out[p] = true
			out[PermAppOverviewRead] = true
			for _, r := range writeImpliesRead(p) {
				out[r] = true
			}
		}
	}
	return out
}

// ExpandedAppPermissions returns the implied fine grants in catalog order,
// without coarse aliases (app:read / app:write / app:admin).
func ExpandedAppPermissions(perms []string) []string {
	exp := ExpandAppPermissions(perms)
	out := make([]string, 0, len(appFinePermissions))
	for _, p := range appFinePermissions {
		if exp[p] {
			out = append(out, p)
		}
	}
	return out
}

// HasAnyAppGrant is true when the token has at least one app permission.
func HasAnyAppGrant(perms []string) bool {
	if len(perms) == 0 {
		return false
	}
	for _, p := range perms {
		if p == "*" || p == "cluster:admin" || KnownAppPermission(p) {
			return true
		}
	}
	return false
}

// HasAppPermission reports whether stored grants cover need. need is a catalog
// id (coarse or fine) or the empty string (any grant on the app).
func HasAppPermission(perms []string, need string) bool {
	if len(perms) == 0 {
		return false
	}
	if need == "" {
		return HasAnyAppGrant(perms)
	}
	if need == permSharedReleaseWrite {
		return HasAppPermission(perms, PermAppEnvWrite) ||
			HasAppPermission(perms, PermAppScaleWrite) ||
			HasAppPermission(perms, PermAppDeploy)
	}
	exp := ExpandAppPermissions(perms)
	if exp[need] {
		return true
	}
	switch need {
	case PermAppRead:
		return exp[PermAppOverviewRead]
	case PermAppWrite:
		for _, p := range perms {
			if p == "*" || p == "cluster:admin" || p == PermAppWrite || p == PermAppAdmin {
				return true
			}
		}
		return hasAll(exp, appManageWritePerms)
	case PermAppAdmin:
		for _, p := range perms {
			if p == "*" || p == "cluster:admin" || p == PermAppAdmin {
				return true
			}
		}
		return exp[PermAppTeamWrite] && hasAll(exp, appManageWritePerms)
	}
	return false
}

// CanCreateRelease is true when the grant may POST /releases (env or scale
// edits). Deploy uses POST /apps/:id/deploy with an existing release id.
func CanCreateRelease(perms []string) bool {
	return HasAppPermission(perms, PermAppEnvWrite) ||
		HasAppPermission(perms, PermAppScaleWrite)
}
