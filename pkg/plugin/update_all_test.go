package plugin

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func officialPluginApp(name string) *ct.App {
	return &ct.App{
		Name: name,
		Meta: map[string]string{MetaPlugin: "true", MetaPluginKind: KindApp},
	}
}

func TestOfficialInstalledFiltersCatalog(t *testing.T) {
	apps := []*ct.App{
		officialPluginApp("redis"),
		officialPluginApp("dashboard"),
		officialPluginApp("pipeline"),
		officialPluginApp("custom-widget"),
		{Name: "postgres"},
	}
	got := OfficialInstalled(apps)
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["redis"] || !names["dashboard"] || !names["pipeline"] || names["custom-widget"] {
		t.Fatalf("names=%v", names)
	}
}

func TestLookupOfficialByAliasAndRepo(t *testing.T) {
	if LookupOfficial(Installed{Name: "mysql"}) == nil {
		t.Fatal("mysql alias")
	}
	if LookupOfficial(Installed{Name: "widget"}) != nil {
		t.Fatal("unknown")
	}
	if LookupOfficial(Installed{Name: "x", GitHubRepo: "randy-girard/flynn-plugin-otel"}) == nil {
		t.Fatal("github repo")
	}
}

func TestRunOfficialUpdatesContinues(t *testing.T) {
	var buf bytes.Buffer
	logf := func(format string, args ...interface{}) {
		fmt.Fprintf(&buf, format+"\n", args...)
	}
	called := []string{}
	err := runInstalledUpdates([]Installed{
		{Name: "redis"},
		{Name: "dashboard"},
		{Name: "otel"},
	}, InstallOptions{AutoTLS: true, AllowExternalLayers: true, Yes: true}, func(opts InstallOptions) error {
		called = append(called, opts.Source)
		if opts.Source == "dashboard" {
			return fmt.Errorf("boom")
		}
		if !opts.AutoTLS || !opts.AllowExternalLayers || !opts.Yes || opts.Source == "" {
			t.Fatalf("%+v", opts)
		}
		return nil
	}, logf)
	if err == nil || !strings.Contains(err.Error(), "1 of 3") {
		t.Fatalf("want partial failure, got %v", err)
	}
	if strings.Join(called, ",") != "redis,dashboard,otel" {
		t.Fatalf("called=%v (must continue past dashboard)", called)
	}
	out := buf.String()
	if !strings.Contains(out, "plugin redis: updated") || !strings.Contains(out, "plugin dashboard: failed: boom") || !strings.Contains(out, "plugin otel: updated") {
		t.Fatalf("log=%q", out)
	}
}

func TestUpdateAllNoClientAndEmpty(t *testing.T) {
	if err := (*Installer)(nil).UpdateAll(InstallOptions{}); err == nil {
		t.Fatal("nil installer")
	}
	in := &Installer{}
	if err := in.UpdateAll(InstallOptions{}); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("missing client: %v", err)
	}
}

func TestUpdateSourceForInstalled(t *testing.T) {
	if got := updateSourceForInstalled(Installed{Name: "pipeline"}); got != "pipeline" {
		t.Fatalf("catalog name: %s", got)
	}
	if got := updateSourceForInstalled(Installed{Name: "widget", GitHubRepo: "acme/flynn-plugin-widget"}); got != "acme/flynn-plugin-widget" {
		t.Fatalf("stamped repo: %s", got)
	}
	if got := updateSourceForInstalled(Installed{Name: "widget", Source: "https://github.com/acme/flynn-plugin-widget.git"}); got != "https://github.com/acme/flynn-plugin-widget.git" {
		t.Fatalf("git source: %s", got)
	}
}

func TestRunInstalledUpdatesIncludesThirdParty(t *testing.T) {
	called := []string{}
	err := runInstalledUpdates([]Installed{
		{Name: "pipeline"},
		{Name: "widget", GitHubRepo: "acme/flynn-plugin-widget"},
	}, InstallOptions{Yes: true}, func(opts InstallOptions) error {
		called = append(called, opts.Source)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(called, ",") != "pipeline,acme/flynn-plugin-widget" {
		t.Fatalf("called=%v", called)
	}
}
