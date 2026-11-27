package authz

import (
	"testing"
)

func TestDefaultAppRolesGrantMapping(t *testing.T) {
	if len(DefaultAppRoles) != 4 {
		t.Fatalf("DefaultAppRoles len=%d, want 4", len(DefaultAppRoles))
	}
	seen := map[string]bool{}
	for _, r := range DefaultAppRoles {
		if seen[r.ID] {
			t.Fatalf("duplicate role id %q", r.ID)
		}
		seen[r.ID] = true
		if r.Name == "" || r.Description == "" {
			t.Fatalf("role %s missing name or description", r.ID)
		}
		got, ok := DefaultRoleByID(r.ID)
		if !ok || got.Name != r.Name {
			t.Fatalf("DefaultRoleByID(%s) = %+v, ok=%v", r.ID, got, ok)
		}
		if len(r.Permissions) < 2 {
			t.Fatalf("role %s should store fine grants, got %v", r.ID, r.Permissions)
		}
	}
	view, _ := DefaultRoleByID("view")
	if !HasAppPermission(view.Permissions, PermAppLogsRead) || HasAppPermission(view.Permissions, PermAppDeploy) {
		t.Fatalf("view grants=%v", view.Permissions)
	}
	deploy, _ := DefaultRoleByID("deploy")
	if !HasAppPermission(deploy.Permissions, PermAppDeploy) || HasAppPermission(deploy.Permissions, PermAppScaleWrite) {
		t.Fatalf("deploy grants=%v", deploy.Permissions)
	}
	manage, _ := DefaultRoleByID("manage")
	if !HasAppPermission(manage.Permissions, PermAppScaleWrite) || HasAppPermission(manage.Permissions, PermAppTeamWrite) {
		t.Fatalf("manage grants=%v", manage.Permissions)
	}
	admin, _ := DefaultRoleByID("admin")
	if !HasAppPermission(admin.Permissions, PermAppTeamWrite) || !HasAppPermission(admin.Permissions, PermAppDelete) {
		t.Fatalf("admin grants=%v", admin.Permissions)
	}
	if CanCreateRelease(view.Permissions) || CanCreateRelease(deploy.Permissions) {
		t.Fatal("view and deploy must not mint releases")
	}
	if !CanCreateRelease(manage.Permissions) || !CanCreateRelease(admin.Permissions) {
		t.Fatal("manage and admin must still mint releases")
	}
	if _, ok := DefaultRoleByID("nope"); ok {
		t.Fatal("unknown role id must not resolve")
	}
}

func TestKnownAppPermission(t *testing.T) {
	for _, p := range AppPermissionCatalog {
		if !KnownAppPermission(p) {
			t.Fatalf("%s must be a known app permission", p)
		}
	}
	if KnownAppPermission("cluster:admin") || KnownAppPermission("app:foo") || KnownAppPermission("") {
		t.Fatal("cluster and invented verbs must not count as app permissions")
	}
}
