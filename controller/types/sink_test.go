package types

import "testing"

func TestParseSinkScope(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"", SinkScopeAll, false},
		{"ALL", SinkScopeAll, false},
		{"system", SinkScopeSystem, false},
		{"apps", SinkScopeApps, false},
		{"app", SinkScopeApps, false},
		{"nope", "", true},
	}
	for _, tc := range cases {
		got, err := ParseSinkScope(tc.in)
		if tc.err {
			if err == nil {
				t.Fatalf("%q: expected error", tc.in)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q %v", tc.in, got, err)
		}
	}
}

func TestAcceptSinkLog(t *testing.T) {
	if !AcceptSinkLog("", "", "app1", false) {
		t.Fatal("all/empty should accept user app")
	}
	if !AcceptSinkLog(SinkScopeAll, "", "app1", true) {
		t.Fatal("all should accept system")
	}
	if AcceptSinkLog(SinkScopeSystem, "", "app1", false) {
		t.Fatal("system scope should drop user apps")
	}
	if !AcceptSinkLog(SinkScopeSystem, "", "sys", true) {
		t.Fatal("system scope should keep system jobs")
	}
	if AcceptSinkLog(SinkScopeApps, "", "sys", true) {
		t.Fatal("apps scope should drop system jobs")
	}
	if !AcceptSinkLog(SinkScopeApps, "", "app1", false) {
		t.Fatal("apps scope should keep user apps")
	}
	if AcceptSinkLog(SinkScopeAll, "app1", "app2", false) {
		t.Fatal("app filter should drop other apps")
	}
	if !AcceptSinkLog(SinkScopeAll, "app1", "app1", false) {
		t.Fatal("app filter should keep matching app")
	}
}
