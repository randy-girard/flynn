package plugin

import "testing"

func TestStampSourceLocalDir(t *testing.T) {
	local := &Resolved{Input: "dashboard", Dir: "/opt/flynn-plugins/flynn-plugin-dashboard"}
	if got := local.StampSource(); got != local.Dir {
		t.Fatalf("alias to local checkout: %q", got)
	}
	path := &Resolved{Input: "../flynn-plugin-dashboard", Dir: "/abs/flynn-plugin-dashboard"}
	if got := path.StampSource(); got != path.Dir {
		t.Fatalf("path install: %q", got)
	}
	gh := &Resolved{Input: "dashboard", GitHub: &GitHubSource{Owner: "randy-girard", Repo: "flynn-plugin-dashboard"}}
	if got := gh.StampSource(); got != "dashboard" {
		t.Fatalf("github install: %q", got)
	}
	if got := (*Resolved)(nil).StampSource(); got != "" {
		t.Fatalf("nil: %q", got)
	}
}
