package authz

import "testing"

func TestMatchOSSAppRole(t *testing.T) {
	cases := []struct {
		perms []string
		id    string
		ok    bool
	}{
		{[]string{PermAppRead}, "view", true},
		{[]string{PermAppDeploy}, "deploy", true},
		{[]string{PermAppWrite}, "manage", true},
		{[]string{PermAppAdmin}, "admin", true},
		{nil, "", false},
		{[]string{}, "", false},
		{[]string{PermAppScaleWrite}, "", false},
		{[]string{PermAppTeamWrite}, "", false},
	}
	for _, tc := range cases {
		got, ok := MatchOSSAppRole(tc.perms)
		if ok != tc.ok {
			t.Fatalf("perms=%v ok=%v want %v", tc.perms, ok, tc.ok)
		}
		if tc.ok && got.ID != tc.id {
			t.Fatalf("perms=%v matched %s want %s", tc.perms, got.ID, tc.id)
		}
	}
	view, _ := DefaultRoleByID("view")
	if !IsOSSPersistableAppPermissions(view.Permissions) {
		t.Fatal("expanded view grants must be persistable")
	}
	if !IsOSSAppRoleID("admin") || IsOSSAppRoleID("qa") || IsOSSAppRoleID("") {
		t.Fatal("OSS role ids")
	}
}
