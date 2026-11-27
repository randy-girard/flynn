package main

import (
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// TestPluginPsqlKeepsPasswordOffArgv (NEW-67, DATA-40): `pg:psql` expands
// ${app.DATABASE_URL} into the job argv, which flynn-host logs in the app's
// "started with command" line and which every process on the host can read.
// The CLI must move the URL's password into PGPASSWORD and strip it from argv.
func TestPluginPsqlKeepsPasswordOffArgv(t *testing.T) {
	old := flagApp
	t.Cleanup(func() { flagApp = old })
	flagApp = "demo"

	const pw = "apppw_argv_secret67"
	pgApp := "pg-abc-12345"
	url := "postgres://app_demo:" + pw + "@leader." + pgApp + ".discoverd:5432/db_demo?sslmode=verify-full"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		"demo": {Env: map[string]string{"FLYNN_POSTGRES": pgApp, "DATABASE_URL": url}},
		pgApp:  {ID: "pg-rel", Env: map[string]string{}},
	}}
	spec := &plugin.CLI{
		Command:     "pg",
		ResourceEnv: "FLYNN_POSTGRES",
		Doc:         "usage: flynn pg psql [--] [<argument>...]",
		Actions: []plugin.CLIAction{{
			Name:   "psql",
			Args:   []string{"psql", "${app.DATABASE_URL}"},
			Append: "<argument>",
			Env:    map[string]string{"PAGER": "less"},
		}},
	}
	args := &docopt.Args{
		Bool:   map[string]bool{"psql": true},
		String: map[string]string{},
		All:    map[string]interface{}{"<argument>": []string{"-c", "SELECT 1"}},
	}
	cfg, err := pluginJobConfig(client, spec, spec.Action("psql"), args)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cfg.Args, " "), pw) {
		t.Fatalf("password on psql argv: %q", cfg.Args)
	}
	want := []string{"psql", "postgres://app_demo@leader." + pgApp + ".discoverd:5432/db_demo?sslmode=verify-full", "-c", "SELECT 1"}
	if strings.Join(cfg.Args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args %q, want %q", cfg.Args, want)
	}
	if cfg.Env["PGPASSWORD"] != pw {
		t.Fatalf("PGPASSWORD=%q", cfg.Env["PGPASSWORD"])
	}
}
