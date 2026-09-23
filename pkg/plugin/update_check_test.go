package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestGitHubSourceForInstalled(t *testing.T) {
	if gh := GitHubSourceForInstalled(Installed{Name: "redis"}, "randy-girard"); gh == nil || gh.Repo != "flynn-plugin-redis" || gh.Owner != "randy-girard" {
		t.Fatalf("official redis: %+v", gh)
	}
	if gh := GitHubSourceForInstalled(Installed{Name: "x", GitHubRepo: "acme/flynn-plugin-widget"}, ""); gh == nil || gh.Owner != "acme" || gh.Repo != "flynn-plugin-widget" {
		t.Fatalf("stamped repo: %+v", gh)
	}
	if gh := GitHubSourceForInstalled(Installed{Name: "x", Source: "https://github.com/other/plug.git"}, ""); gh == nil || gh.Owner != "other" || gh.Repo != "plug" {
		t.Fatalf("source url: %+v", gh)
	}
	if gh := GitHubSourceForInstalled(Installed{Name: "custom-local"}, ""); gh != nil {
		t.Fatalf("unknown plugin: %+v", gh)
	}
}

func TestWriteHostPluginTableCheck(t *testing.T) {
	rows := []ListedPlugin{
		{
			Installed: Installed{Name: "dashboard", Kind: KindApp, Source: "src", Ref: "v20260919.0.1", CLI: &CLI{Command: "dashboard"}},
			Available: "v20260919.0.3",
			Status:    UpdateStatusUpdate,
		},
		{
			Installed: Installed{Name: "redis", Kind: KindResourceProvider, Ref: "v20260919.0.3"},
			Available: "v20260919.0.3",
			Status:    UpdateStatusCurrent,
		},
	}
	var buf strings.Builder
	if err := WriteHostPluginTable(&buf, rows, true); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"VERSION", "UPDATE", "STATUS", "dashboard", "v20260919.0.1", "v20260919.0.3", "update", "redis", "current"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if CountPluginUpdates(rows) != 1 {
		t.Fatalf("count=%d", CountPluginUpdates(rows))
	}
	var notes strings.Builder
	WritePluginUpdateNotes(&notes, rows)
	if !strings.Contains(notes.String(), "1 plugin(s) can be updated") {
		t.Fatalf("notes=%q", notes.String())
	}
}

func TestCheckUpdatesPicksCompatibleTag(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/randy-girard/flynn-plugin-dashboard/releases", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]githubRelease{
			{TagName: "v20260919.0.1"},
			{TagName: "v20260919.0.3"},
			{TagName: "v20260920.0.0"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	in := &Installer{GitHubHTTP: srv.Client(), FlynnVersion: "v20260919.0"}
	rec := Installed{Name: "dashboard", Ref: "v20260919.0.1", GitHubRepo: "randy-girard/flynn-plugin-dashboard"}
	row := in.checkOneUpdateWithAPI(rec, srv.URL)
	if row.Status != UpdateStatusUpdate || row.Available != "v20260919.0.3" {
		t.Fatalf("%+v", row)
	}
	rec.Ref = "v20260919.0.3"
	row = in.checkOneUpdateWithAPI(rec, srv.URL)
	if row.Status != UpdateStatusCurrent || row.Available != "v20260919.0.3" {
		t.Fatalf("current %+v", row)
	}
}

func (in *Installer) checkOneUpdateWithAPI(p Installed, api string) ListedPlugin {
	row := ListedPlugin{Installed: p, Status: UpdateStatusUnknown}
	src := GitHubSourceForInstalled(p, "")
	if src == nil {
		return row
	}
	src.API = api
	rel, err := in.latestCalVerRelease(src, "")
	if err != nil {
		row.Status = UpdateStatusError
		row.Err = err
		return row
	}
	row.Available = rel.TagName
	if PluginNeedsUpdate(p.Ref, row.Available) {
		row.Status = UpdateStatusUpdate
	} else {
		row.Status = UpdateStatusCurrent
	}
	return row
}

func TestListedFromInstalledAndUserTable(t *testing.T) {
	apps := []*ct.App{{
		Name: "redis",
		Meta: map[string]string{
			MetaPlugin:     "true",
			MetaPluginKind: KindResourceProvider,
			MetaPluginRef:  "v20260915.0",
			MetaPluginCLI:  `{"command":"redis","usage":"manage redis"}`,
		},
	}}
	rows := ListedFromInstalled(ListInstalled(apps))
	if len(rows) != 1 || rows[0].Version() != "v20260915.0" {
		t.Fatalf("%+v", rows)
	}
	var buf strings.Builder
	if err := WriteUserPluginTable(&buf, rows, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "VERSION") || strings.Contains(buf.String(), "UPDATE") {
		t.Fatalf("%s", buf.String())
	}
}
