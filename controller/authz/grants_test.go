package authz

import "testing"

func TestHasAppPermissionGranular(t *testing.T) {
	if !HasAppPermission([]string{PermAppLogsRead}, PermAppLogsRead) {
		t.Fatal("logs:read should cover logs:read")
	}
	if HasAppPermission([]string{PermAppLogsRead}, PermAppScaleRead) {
		t.Fatal("logs:read must not cover scale:read")
	}
	if !HasAppPermission([]string{PermAppScaleWrite}, PermAppScaleRead) {
		t.Fatal("scale:write implies scale:read")
	}
	if HasAppPermission([]string{PermAppWrite}, PermAppTeamWrite) {
		t.Fatal("manage must not cover team:write")
	}
	if !HasAppPermission([]string{PermAppAdmin}, PermAppTeamWrite) {
		t.Fatal("admin covers team:write")
	}
	if !CanCreateRelease([]string{PermAppEnvWrite}) {
		t.Fatal("env:write may POST /releases")
	}
	if CanCreateRelease([]string{PermAppDeploy}) {
		t.Fatal("deploy must not POST /releases")
	}
	view := ExpandedAppPermissions([]string{PermAppRead})
	if len(view) < 2 || !HasAppPermission(view, PermAppLogsRead) || HasAppPermission(view, PermAppDeploy) {
		t.Fatalf("view expansion=%v", view)
	}
}
