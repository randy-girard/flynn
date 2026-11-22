package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

type fakeCrossAppAPI struct {
	rows []*ct.CrossAppRelease
	err  error
	path string
}

func (f *fakeCrossAppAPI) Get(path string, out interface{}) error {
	f.path = path
	if f.err != nil {
		return f.err
	}
	*(out.(*[]*ct.CrossAppRelease)) = f.rows
	return nil
}

func crossAppFixture() []*ct.CrossAppRelease {
	return []*ct.CrossAppRelease{
		{Kind: ct.CrossAppKindCurrentRelease, AppID: "a1", AppName: "mine", ReleaseID: "r-other", ReleaseAppID: "a2", ReleaseAppName: "other"},
		{Kind: ct.CrossAppKindFormation, AppID: "a1", AppName: "mine", ReleaseID: "r-other", ReleaseAppID: "a2", ReleaseAppName: "other", Processes: map[string]int{"web": 2, "worker": 1}},
		{Kind: ct.CrossAppKindFormation, AppID: "s1", AppName: "sysapp", ReleaseID: "r-sys", ReleaseAppID: "s2", ReleaseAppName: "router", System: true, Processes: map[string]int{"app": 1}},
	}
}

func TestCrossAppCheckRegistered(t *testing.T) {
	if commands["cross-app:check"] == nil {
		t.Fatal("missing cross-app:check")
	}
	name, args, from := ResolveCommand("cross-app", []string{"check", "--all"})
	if name != "cross-app:check" || from != "cross-app check" || len(args) != 1 || args[0] != "--all" {
		t.Fatalf("space alias got %q %q from=%q", name, args, from)
	}
}

// TestCrossAppCheckListsRowsAndFails: CORE-52. Non-system rows are printed
// with the commands that clear them and the check exits non-zero. System
// rows are hidden without --all.
func TestCrossAppCheckListsRowsAndFails(t *testing.T) {
	api := &fakeCrossAppAPI{rows: crossAppFixture()}
	var out bytes.Buffer
	err := runCrossAppCheckWith(&out, api, false)
	if err == nil {
		t.Fatal("expected a failure when cross-app rows exist")
	}
	if api.path != "/cluster/cross-app-releases" {
		t.Fatalf("path %q", api.path)
	}
	s := out.String()
	for _, want := range []string{
		"current_release", "formation", "mine", "r-other", "other", "web=2,worker=1",
		"flynn -a mine scale --release r-other web=0 worker=0",
		"flynn -a mine release:rollback",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "sysapp") {
		t.Fatalf("system row shown without --all:\n%s", s)
	}

	out.Reset()
	_ = runCrossAppCheckWith(&out, api, true)
	if !strings.Contains(out.String(), "sysapp") {
		t.Fatalf("--all must show system rows:\n%s", out.String())
	}
}

func TestCrossAppCheckCleanAndSystemOnlyPass(t *testing.T) {
	var out bytes.Buffer
	if err := runCrossAppCheckWith(&out, &fakeCrossAppAPI{}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No app runs or serves another app's release") {
		t.Fatalf("clean output: %q", out.String())
	}
	out.Reset()
	sysOnly := &fakeCrossAppAPI{rows: crossAppFixture()[2:]}
	if err := runCrossAppCheckWith(&out, sysOnly, false); err != nil {
		t.Fatalf("system-only rows must not fail: %v", err)
	}
	if err := runCrossAppCheckWith(&out, &fakeCrossAppAPI{err: errors.New("boom")}, false); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("controller error: %v", err)
	}
}

// TestUpdateWarnsAboutCrossAppReleases: flynn-host update prints a warning
// (never an error) after system apps are deployed, and is quiet on a clean
// cluster or an older controller without the route.
func TestUpdateWarnsAboutCrossAppReleases(t *testing.T) {
	var out bytes.Buffer
	warnCrossAppReleases(&out, &fakeCrossAppAPI{rows: crossAppFixture()})
	s := out.String()
	if !strings.Contains(s, "WARNING") || !strings.Contains(s, "flynn-host cross-app:check") || !strings.Contains(s, "1 app") {
		t.Fatalf("warning: %q", s)
	}
	out.Reset()
	warnCrossAppReleases(&out, &fakeCrossAppAPI{})
	warnCrossAppReleases(&out, &fakeCrossAppAPI{rows: crossAppFixture()[2:]})
	warnCrossAppReleases(&out, &fakeCrossAppAPI{err: ct.ErrNotFound})
	warnCrossAppReleases(&out, nil)
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}
}
