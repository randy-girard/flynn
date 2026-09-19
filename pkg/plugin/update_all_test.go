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
		officialPluginApp("custom-widget"),
		{Name: "postgres"},
	}
	got := OfficialInstalled(apps)
	if len(got) != 2 {
		t.Fatalf("got %#v", got)
	}
	names := map[string]bool{}
	for _, p := range got {
		names[p.Name] = true
	}
	if !names["redis"] || !names["dashboard"] || names["custom-widget"] {
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
	err := runOfficialUpdates([]Installed{
		{Name: "redis"},
		{Name: "dashboard"},
		{Name: "otel"},
	}, InstallOptions{AutoTLS: true}, func(opts InstallOptions) error {
		called = append(called, opts.Source)
		if opts.Source == "dashboard" {
			return fmt.Errorf("boom")
		}
		if !opts.AutoTLS || opts.Source == "" {
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
