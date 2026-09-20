package authz

// Named app roles used by the dashboard Team picker and documented for
// `flynn login` tokens. Selecting a role grants these controller permissions
// on that app; HTTPAllowed (and the CLI, which calls the same API) then
// enforce the grants. Custom cluster roles may combine coarse aliases and
// function/action grants (app:<function>:<action>).
const (
	PermAppRead   = "app:read"
	PermAppDeploy = "app:deploy"
	PermAppWrite  = "app:write"
	PermAppAdmin  = "app:admin"
)

// AppRole is a named bundle of controller app grants.
type AppRole struct {
	ID          string
	Name        string
	Description string
	Permissions []string
}

// DefaultAppRoles are created automatically for every cluster. IDs are stable
// so dashboard pickers and docs can refer to them.
var DefaultAppRoles = []AppRole{
	{
		ID:          "view",
		Name:        "View",
		Description: "View every app function. Cannot change anything.",
		Permissions: []string{PermAppRead},
	},
	{
		ID:          "deploy",
		Name:        "Deploy",
		Description: "Ship a new release. Cannot change config, scale, or team.",
		Permissions: []string{PermAppDeploy},
	},
	{
		ID:          "manage",
		Name:        "Manage",
		Description: "Change config, scale, routes, and deploy. Cannot manage team.",
		Permissions: []string{PermAppWrite},
	},
	{
		ID:          "admin",
		Name:        "Admin",
		Description: "Full app access, including team invites and collaborator roles.",
		Permissions: []string{PermAppAdmin},
	},
}

// AppPermissionCatalog is the set of controller grants a custom role may include
// (coarse aliases plus function/action grants).
var AppPermissionCatalog = []string{
	PermAppRead, PermAppDeploy, PermAppWrite, PermAppAdmin,
	PermAppOverviewRead,
	PermAppScaleRead, PermAppScaleWrite,
	PermAppEnvRead, PermAppEnvWrite,
	PermAppLogsRead,
	PermAppMetricsRead,
	PermAppAlertsRead, PermAppAlertsWrite,
	PermAppJobsRead, PermAppJobsRun, PermAppJobsStop,
	PermAppSchedulerRead, PermAppSchedulerWrite,
	PermAppActivityRead,
	PermAppRoutesRead, PermAppRoutesWrite,
	PermAppResourcesRead, PermAppResourcesWrite,
	PermAppGitHubRead, PermAppGitHubWrite,
	PermAppTeamRead, PermAppTeamWrite,
	PermAppDelete,
}

var knownAppPerms map[string]bool

func init() {
	knownAppPerms = make(map[string]bool, len(AppPermissionCatalog))
	for _, p := range AppPermissionCatalog {
		knownAppPerms[p] = true
	}
	for i := range DefaultAppRoles {
		DefaultAppRoles[i].Permissions = ExpandedAppPermissions(DefaultAppRoles[i].Permissions)
	}
}

// KnownAppPermission reports whether p is a controller app grant.
func KnownAppPermission(p string) bool {
	return knownAppPerms[p]
}

// DefaultRoleByID returns a built-in app role.
func DefaultRoleByID(id string) (AppRole, bool) {
	for _, r := range DefaultAppRoles {
		if r.ID == id {
			return r, true
		}
	}
	return AppRole{}, false
}
