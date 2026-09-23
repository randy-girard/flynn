package authz

import "strings"

// ErrGranularRBAC is returned by OSS APIs that would persist a custom grant
// bundle. Fine-grained strings remain valid on tokens (the four aliases expand
// to them); only the enterprise plugin may mint arbitrary combinations.
const ErrGranularRBACMessage = "granular RBAC requires the enterprise plugin"

// MatchOSSAppRole reports whether perms are exactly one of the four built-in
// app roles (View/Deploy/Manage/Admin), as a coarse alias or the expanded
// function/action set stored after EnsureDefaultAppRoles.
func MatchOSSAppRole(perms []string) (AppRole, bool) {
	exp := ExpandedAppPermissions(perms)
	if len(exp) == 0 {
		return AppRole{}, false
	}
	for _, r := range DefaultAppRoles {
		if sameGrantSet(exp, r.Permissions) {
			return r, true
		}
	}
	return AppRole{}, false
}

// IsOSSPersistableAppPermissions is true when an API may store these grants
// without the enterprise plugin.
func IsOSSPersistableAppPermissions(perms []string) bool {
	_, ok := MatchOSSAppRole(perms)
	return ok
}

// IsOSSAppRoleID is true for the four frozen role ids.
func IsOSSAppRoleID(id string) bool {
	_, ok := DefaultRoleByID(strings.TrimSpace(id))
	return ok
}

func sameGrantSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, p := range a {
		seen[p] = true
	}
	for _, p := range b {
		if !seen[p] {
			return false
		}
	}
	return true
}
