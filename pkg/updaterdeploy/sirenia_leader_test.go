package updaterdeploy

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestSireniaApplianceServices(t *testing.T) {
	t.Setenv("FLYNN_INSTALLED_PLUGINS", filepath.Join(t.TempDir(), "none.json"))
	got := SireniaApplianceServices()
	if len(got) != 1 || got[0] != "postgres" {
		t.Fatalf("core-only inventory: %v", got)
	}
	path := filepath.Join(t.TempDir(), "installed.json")
	t.Setenv("FLYNN_INSTALLED_PLUGINS", path)
	if err := plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "widget", Sirenia: true},
	}); err != nil {
		t.Fatal(err)
	}
	got = SireniaApplianceServices()
	if len(got) != 2 || got[0] != "postgres" || got[1] != "widget" {
		t.Fatalf("got %v", got)
	}
}

func TestSkipOptionalSireniaLeaderWait(t *testing.T) {
	t.Setenv("FLYNN_INSTALLED_PLUGINS", filepath.Join(t.TempDir(), "none.json"))
	cases := []struct {
		service       string
		instanceCount int
		instancesErr  error
		want          bool
	}{
		{"postgres", 0, errors.New("not found"), false},
		{"postgres", 0, nil, false},
		{"mariadb", 0, nil, true},
		{"mariadb", 0, errors.New("service not found"), true},
		{"mariadb", 1, nil, false},
		{"mongodb", 0, nil, true},
		{"mongodb", 2, nil, false},
		{"redis", 0, nil, true},
	}
	for _, tc := range cases {
		got := skipOptionalSireniaLeaderWait(tc.service, tc.instanceCount, tc.instancesErr)
		if got != tc.want {
			t.Fatalf("skipOptionalSireniaLeaderWait(%q, %d, %v): got %v want %v",
				tc.service, tc.instanceCount, tc.instancesErr, got, tc.want)
		}
	}
}
