package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func redisPluginCLI() *plugin.CLI {
	return &plugin.CLI{
		Command:         "redis",
		Usage:           "manage redis databases",
		ResourceEnv:     "FLYNN_REDIS",
		ResourceMissing: "No redis server found. Provision one with `flynn resource:add redis`",
		Doc:             "usage: flynn redis redis-cli [--] [<argument>...]",
		Actions: []plugin.CLIAction{{
			Name:   "redis-cli",
			Args:   []string{"redis-cli", "-h", "${app.REDIS_HOST|leader.${resource}.discoverd}", "-a", "${app.REDIS_PASSWORD}"},
			Append: "<argument>",
		}},
	}
}

type fakeRedisReleaseClient struct {
	releases map[string]*ct.Release
}

func (f fakeRedisReleaseClient) GetAppRelease(appID string) (*ct.Release, error) {
	if r, ok := f.releases[appID]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("no release for %s", appID)
}

func TestPluginInterpRedisDialsLeaderDiscoverd(t *testing.T) {
	redisApp := "redis-11111111-2222-3333-4444-555555555555"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		redisApp: {ID: "redis-release"},
	}}
	in, rel, err := pluginInterp(client, redisPluginCLI(), &ct.Release{
		Env: map[string]string{
			"FLYNN_REDIS":    redisApp,
			"REDIS_PASSWORD": "s3cret",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.ID != "redis-release" {
		t.Fatalf("Release=%q", rel.ID)
	}
	args, err := plugin.InterpolateAll(redisPluginCLI().Action("redis-cli").Args, in)
	if err != nil {
		t.Fatal(err)
	}
	wantHost := "leader." + redisApp + ".discoverd"
	if !containsArgPair(args, "-h", wantHost) {
		t.Fatalf("redis-cli args %q must pass -h %s", strings.Join(args, " "), wantHost)
	}
	if strings.Contains(strings.Join(args, " "), " "+redisApp+".discoverd") {
		t.Fatalf("must not dial %s.discoverd: %q", redisApp, args)
	}
	if !containsArgPair(args, "-a", "s3cret") {
		t.Fatalf("password: %q", args)
	}
}

func TestPluginInterpRedisUsesREDISHost(t *testing.T) {
	redisApp := "redis-11111111-2222-3333-4444-555555555555"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		redisApp: {ID: "redis-release"},
	}}
	host := "leader." + redisApp + ".discoverd"
	in, _, err := pluginInterp(client, redisPluginCLI(), &ct.Release{
		Env: map[string]string{
			"FLYNN_REDIS":    redisApp,
			"REDIS_HOST":     host,
			"REDIS_PASSWORD": "s3cret",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	args, err := plugin.InterpolateAll(redisPluginCLI().Action("redis-cli").Args, in)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgPair(args, "-h", host) {
		t.Fatalf("args %q must pass provisioned REDIS_HOST", strings.Join(args, " "))
	}
}

func TestPluginInterpRequiresResource(t *testing.T) {
	_, _, err := pluginInterp(fakeRedisReleaseClient{}, redisPluginCLI(), &ct.Release{Env: map[string]string{}}, "")
	if err == nil || !strings.Contains(err.Error(), "No redis server found") {
		t.Fatalf("got %v", err)
	}

	_, _, err = pluginInterp(fakeRedisReleaseClient{}, &plugin.CLI{Command: "widget"}, &ct.Release{}, "")
	if err == nil || !strings.Contains(err.Error(), "does not declare a resource") {
		t.Fatalf("got %v", err)
	}

	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{"widget": {ID: "rel"}}}
	in, rel, err := pluginInterp(client, &plugin.CLI{Command: "widget", App: "widget"}, &ct.Release{}, "")
	if err != nil || in.Resource != "widget" || rel.ID != "rel" {
		t.Fatalf("app-named plugin: %+v %v %v", in, rel, err)
	}
}

func TestPluginJobConfigAndIO(t *testing.T) {
	old := flagApp
	t.Cleanup(func() { flagApp = old })
	flagApp = "demo"

	redisApp := "redis-abc"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		"demo":   {Env: map[string]string{"FLYNN_REDIS": redisApp, "REDIS_PASSWORD": "s3cret"}},
		redisApp: {ID: "redis-rel", Env: map[string]string{"REDIS_PASSWORD": "appliance"}},
	}}
	spec := redisPluginCLI()
	spec.Actions[0].Env = map[string]string{"PAGER": "less"}
	args := &docopt.Args{
		Bool:   map[string]bool{"redis-cli": true, "--quiet": true},
		String: map[string]string{},
		All:    map[string]interface{}{"<argument>": []string{"PING"}},
	}
	cfg, err := pluginJobConfig(client, spec, spec.Action("redis-cli"), args)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != "demo" || cfg.Release != "redis-rel" || !cfg.DisableLog || !cfg.Exit {
		t.Fatalf("%+v", cfg)
	}
	if cfg.App == redisApp {
		t.Fatal("native redis-cli must run on the user app, not the database appliance")
	}
	if cfg.Partition != ct.PartitionTypeSystem {
		t.Fatalf("plugin CLI jobs must use the system partition so they can resolve plugin APIs, got %q", cfg.Partition)
	}
	if !containsArgPair(cfg.Args, "-a", "s3cret") {
		t.Fatalf("args=%q", cfg.Args)
	}
	if cfg.Args[len(cfg.Args)-1] != "PING" {
		t.Fatalf("append: %q", cfg.Args)
	}
	if cfg.Env["PAGER"] != "less" {
		t.Fatalf("env=%v", cfg.Env)
	}

	empty := fakeRedisReleaseClient{releases: map[string]*ct.Release{"demo": {Env: map[string]string{}}}}
	if _, err = pluginJobConfig(empty, redisPluginCLI(), redisPluginCLI().Action("redis-cli"), args); err == nil {
		t.Fatal("missing resource must fail")
	}

	dump := filepath.Join(t.TempDir(), "db.dump")
	ioArgs := &docopt.Args{
		Bool:   map[string]bool{"--quiet": true},
		String: map[string]string{"--file": dump},
	}
	cfg = &runConfig{}
	cleanup, err := pluginJobIO(cfg, &plugin.CLIAction{StdoutFile: "--file", Quiet: "--quiet", Progress: true}, ioArgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Stdout.Write([]byte("RDB")); err != nil {
		t.Fatal(err)
	}
	cleanup()
	got, err := os.ReadFile(dump)
	if err != nil || string(got) != "RDB" {
		t.Fatalf("dump file %q %v", got, err)
	}

	restore := filepath.Join(t.TempDir(), "in.dump")
	if err := os.WriteFile(restore, []byte("IN"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg = &runConfig{}
	cleanup, err = pluginJobIO(cfg, &plugin.CLIAction{StdinFile: "--file", Quiet: "--quiet"}, &docopt.Args{
		Bool:   map[string]bool{"--quiet": true},
		String: map[string]string{"--file": restore},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(cfg.Stdin)
	cleanup()
	if err != nil || string(body) != "IN" {
		t.Fatalf("restore stdin %q %v", body, err)
	}

	if _, err := pluginJobIO(&runConfig{}, &plugin.CLIAction{StdoutFile: "--file"}, &docopt.Args{
		String: map[string]string{"--file": filepath.Join(t.TempDir(), "missing-dir", "x")},
	}); err == nil {
		t.Fatal("create in missing dir must fail")
	}
}

func TestExecutePluginCLINoMatchingAction(t *testing.T) {
	err := executePluginCLI(nil, redisPluginCLI(), &docopt.Args{Bool: map[string]bool{}}, nil)
	if err == nil || !strings.Contains(err.Error(), "no matching plugin CLI action") {
		t.Fatalf("got %v", err)
	}
}

func TestRunPluginFlynnCommand(t *testing.T) {
	spec := &plugin.CLI{Command: "widget", App: "widget"}
	err := runPluginFlynnCommand(nil, spec, &plugin.CLIAction{Name: "route", Flynn: "not-a-flynn-cmd"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not a built-in CLI command") {
		t.Fatalf("unknown flynn cmd: %v", err)
	}
	err = runPluginFlynnCommand(nil, &plugin.CLI{Command: "widget"}, &plugin.CLIAction{Name: "route", Flynn: "route"}, nil)
	if err == nil || !strings.Contains(err.Error(), "plugin app name") {
		t.Fatalf("missing app: %v", err)
	}
	err = runPluginFlynnCommand(nil, spec, &plugin.CLIAction{Flynn: ""}, nil)
	if err == nil || !strings.Contains(err.Error(), "missing flynn command") {
		t.Fatalf("empty flynn: %v", err)
	}
}

func TestExecutePluginCLIFlynnDelegate(t *testing.T) {
	spec := &plugin.CLI{
		Command: "widget",
		App:     "widget",
		Actions: []plugin.CLIAction{{Name: "route", Flynn: "not-a-flynn-cmd"}},
	}
	err := executePluginCLI(nil, spec, &docopt.Args{Bool: map[string]bool{"route": true}}, []string{"route"})
	if err == nil || !strings.Contains(err.Error(), "not a built-in CLI command") {
		t.Fatalf("got %v", err)
	}
}

func TestPluginJobConfigClusterUsesPluginApp(t *testing.T) {
	old := flagApp
	t.Cleanup(func() { flagApp = old })
	flagApp = ""

	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		"enterprise": {ID: "ent-rel", Env: map[string]string{"ENTERPRISE_URL": "http://enterprise.discoverd"}},
	}}
	spec := &plugin.CLI{
		Command: "enterprise",
		App:     "enterprise",
		Actions: []plugin.CLIAction{{
			Name:    "show",
			Args:    []string{"/bin/enterprise-cli"},
			Cluster: true,
		}},
	}
	cfg, err := pluginJobConfig(client, spec, spec.Action("show"), &docopt.Args{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != "enterprise" || cfg.Release != "ent-rel" {
		t.Fatalf("%+v", cfg)
	}
}

func TestPluginJobConfigErrors(t *testing.T) {
	old := flagApp
	t.Cleanup(func() { flagApp = old })
	flagApp = "demo"
	spec := redisPluginCLI()
	args := &docopt.Args{Bool: map[string]bool{"redis-cli": true}, All: map[string]interface{}{}}

	if _, err := pluginJobConfig(fakeRedisReleaseClient{}, spec, spec.Action("redis-cli"), args); err == nil || !strings.Contains(err.Error(), "error getting app release") {
		t.Fatalf("missing app release: %v", err)
	}

	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		"demo":      {Env: map[string]string{"FLYNN_REDIS": "redis-abc"}},
		"redis-abc": {ID: ""},
	}}
	if _, err := pluginJobConfig(client, spec, spec.Action("redis-cli"), args); err == nil || !strings.Contains(err.Error(), "error getting redis release") {
		t.Fatalf("empty resource release: %v", err)
	}

	client = fakeRedisReleaseClient{releases: map[string]*ct.Release{
		"demo":      {Env: map[string]string{"FLYNN_REDIS": "redis-abc"}},
		"redis-abc": {ID: "rel"},
	}}
	bad := spec.Action("redis-cli")
	bad.Env = map[string]string{"X": "${nope}"}
	if _, err := pluginJobConfig(client, spec, bad, args); err == nil {
		t.Fatal("bad interpolate")
	}
}

func TestPluginInterpDefaultMissingAndReleaseError(t *testing.T) {
	spec := &plugin.CLI{Command: "cache", ResourceEnv: "FLYNN_CACHE"}
	_, _, err := pluginInterp(fakeRedisReleaseClient{}, spec, &ct.Release{Env: map[string]string{}}, "")
	if err == nil || !strings.Contains(err.Error(), "flynn resource:add cache") {
		t.Fatalf("default missing: %v", err)
	}

	_, _, err = pluginInterp(fakeRedisReleaseClient{}, &plugin.CLI{Command: "cache", App: "cache"}, nil, "")
	if err == nil || !strings.Contains(err.Error(), "error getting cache release") {
		t.Fatalf("release lookup: %v", err)
	}
}

func TestPluginJobIOStdoutDefaultAndMissingStdin(t *testing.T) {
	cfg := &runConfig{}
	cleanup, err := pluginJobIO(cfg, &plugin.CLIAction{StdoutFile: "--file"}, &docopt.Args{String: map[string]string{"--file": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Stdout == nil {
		t.Fatal("stdout")
	}
	cleanup()

	if _, err := pluginJobIO(&runConfig{}, &plugin.CLIAction{StdinFile: "--file"}, &docopt.Args{
		String: map[string]string{"--file": filepath.Join(t.TempDir(), "missing")},
	}); err == nil {
		t.Fatal("missing stdin file")
	}
}

func TestPluginInterpPasswordNotRescanned(t *testing.T) {
	redisApp := "redis-abc"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		redisApp: {ID: "rel"},
	}}
	in, _, err := pluginInterp(client, redisPluginCLI(), &ct.Release{
		Env: map[string]string{
			"FLYNN_REDIS":    redisApp,
			"REDIS_PASSWORD": "x${resource}y",
		},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	args, err := plugin.InterpolateAll(redisPluginCLI().Action("redis-cli").Args, in)
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgPair(args, "-a", "x${resource}y") {
		t.Fatalf("password re-expanded: %q", args)
	}
}

func TestPluginMatchActionPrefersLongestName(t *testing.T) {
	spec := &plugin.CLI{Actions: []plugin.CLIAction{
		{Name: "topics", Args: []string{"list"}},
		{Name: "topics create", Args: []string{"create"}},
		{Name: "consumer-groups create", Args: []string{"cg-create"}},
	}}
	got := spec.MatchAction(map[string]bool{"topics": true, "create": true})
	if got == nil || got.Name != "topics create" {
		t.Fatalf("got %+v", got)
	}
	got = spec.MatchAction(map[string]bool{"consumer-groups": true, "create": true})
	if got == nil || got.Name != "consumer-groups create" {
		t.Fatalf("got %+v", got)
	}
	got = spec.MatchAction(map[string]bool{"topics": true})
	if got == nil || got.Name != "topics" {
		t.Fatalf("got %+v", got)
	}
}

func TestPeelResourceNameLeavesConsoleArgs(t *testing.T) {
	name, rest := peelResourceName("redis", []string{"redis-cli", "PING"})
	if name != "" || len(rest) != 2 || rest[1] != "PING" {
		t.Fatalf("PING must stay an argument, got name %q rest %#v", name, rest)
	}
	name, rest = peelResourceName("pg", []string{"psql", "pg-harbor-kxmnpq", "--", "-c", "SELECT 1"})
	if name != "pg-harbor-kxmnpq" || strings.Join(rest, " ") != "psql -- -c SELECT 1" {
		t.Fatalf("name %q rest %#v", name, rest)
	}
	name, rest = peelResourceName("pg", []string{"psql", "--", "pg-harbor-kxmnpq"})
	if name != "" {
		t.Fatalf("tokens after -- are arguments: %q", name)
	}
}

func TestPluginInterpNamedResource(t *testing.T) {
	named := "pg-harbor-kxmnpq"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		named: {ID: "pg-rel", Env: map[string]string{
			"POSTGRES_URL":      "postgres://app:pw@leader." + named + ".discoverd:5432/db?sslmode=require",
			"POSTGRES_PASSWORD": "pw",
		}},
	}}
	in, rel, err := pluginInterp(client, &plugin.CLI{Command: "pg", ResourceEnv: "FLYNN_POSTGRES"}, &ct.Release{
		Env: map[string]string{
			"FLYNN_POSTGRES": "pg-other-aaaaaa",
			"POSTGRES_URL":   "postgres://wrong",
		},
	}, "harbor-kxmnpq")
	if err != nil {
		t.Fatal(err)
	}
	if in.Resource != named || rel.ID != "pg-rel" {
		t.Fatalf("resource %q release %v", in.Resource, rel)
	}
	if in.App["POSTGRES_URL"] != client.releases[named].Env["POSTGRES_URL"] {
		t.Fatalf("named URL: %q", in.App["POSTGRES_URL"])
	}
	args, err := plugin.InterpolateAll([]string{"psql", "${app.POSTGRES_URL}"}, in)
	if err != nil {
		t.Fatal(err)
	}
	if args[1] != in.App["POSTGRES_URL"] {
		t.Fatalf("psql %q", args)
	}
}

func TestApplyNamedResourcePrefersMysqlAppPassword(t *testing.T) {
	app := map[string]string{
		"MYSQL_HOST": "leader.mysql-orchid-aaaaaa.discoverd",
		"MYSQL_USER": "app_leader",
		"MYSQL_PWD":  "apppw_leader",
	}
	applyNamedResource(app, "mysql-fjord-bbbbbb", map[string]string{
		"MYSQL_USER":         "app_leader",
		"MYSQL_PWD":          "supw_secret",
		"MYSQL_APP_PASSWORD": "apppw_leader",
	})
	if app["MYSQL_PWD"] != "apppw_leader" {
		t.Fatalf("MYSQL_PWD=%q, want the app user not the superuser", app["MYSQL_PWD"])
	}
	if app["MYSQL_HOST"] != "leader.mysql-fjord-bbbbbb.discoverd" {
		t.Fatalf("MYSQL_HOST=%q", app["MYSQL_HOST"])
	}
}

func TestApplyNamedResourcePrefersMongoAppPassword(t *testing.T) {
	app := map[string]string{
		"MONGO_HOST": "leader.mongodb-orchid-aaaaaa.discoverd",
		"MONGO_USER": "app_leader",
		"MONGO_PWD":  "apppw_leader",
	}
	applyNamedResource(app, "mongodb-fjord-bbbbbb", map[string]string{
		"MONGO_USER":         "app_leader",
		"MONGO_PWD":          "supw_secret",
		"MONGO_APP_PASSWORD": "apppw_leader",
	})
	if app["MONGO_PWD"] != "apppw_leader" {
		t.Fatalf("MONGO_PWD=%q, want the app user not the superuser", app["MONGO_PWD"])
	}
}

func TestApplyNamedResourceClickhouseUsesFollowerAdmin(t *testing.T) {
	app := map[string]string{
		"CLICKHOUSE_HOST":         "leader.clickhouse-quartz-aaaaaa.discoverd",
		"CLICKHOUSE_USER":         "u_clickhouse-quartz-aaaaaa",
		"CLICKHOUSE_PASSWORD":     "tenant-leader",
		"CLICKHOUSE_TRUSTED_CERT": "leader-ca",
		"CLICKHOUSE_URL":          "clickhouses://u_leader@leader.clickhouse-quartz-aaaaaa.discoverd:9440",
	}
	applyNamedResource(app, "clickhouse-willow-bbbbbb", map[string]string{
		"CLICKHOUSE_PASSWORD":     "admin-follower",
		"CLICKHOUSE_TRUSTED_CERT": "follower-ca",
		"FLYNN_CLICKHOUSE":        "clickhouse-willow-bbbbbb",
	})
	if app["CLICKHOUSE_HOST"] != "leader.clickhouse-willow-bbbbbb.discoverd" {
		t.Fatalf("host %q", app["CLICKHOUSE_HOST"])
	}
	if app["CLICKHOUSE_USER"] != "" {
		t.Fatalf("user %q, want default interpolation", app["CLICKHOUSE_USER"])
	}
	if app["CLICKHOUSE_PASSWORD"] != "admin-follower" {
		t.Fatalf("password %q", app["CLICKHOUSE_PASSWORD"])
	}
	if app["CLICKHOUSE_TRUSTED_CERT"] != "follower-ca" {
		t.Fatalf("tls ca %q", app["CLICKHOUSE_TRUSTED_CERT"])
	}
}

func TestApplyNamedResourceKafkaUsesFollowerBootstrap(t *testing.T) {
	app := map[string]string{
		"KAFKA_BOOTSTRAP_SERVERS": "leader.kafka-quartz-aaaaaa.discoverd:9092",
		"KAFKA_TRUSTED_CERT":      "leader-ca",
		"KAFKA_SASL_USERNAME":     "u-leader",
		"KAFKA_SASL_PASSWORD":     "leader-pw",
	}
	applyNamedResource(app, "kafka-willow-bbbbbb", map[string]string{
		"KAFKA_SUPERUSER_PASSWORD": "super-secret",
		"KAFKA_TRUSTED_CERT":       "follower-ca",
		"KAFKA_CLIENT_CERT":        "follower-cert",
		"KAFKA_CLIENT_CERT_KEY":    "follower-key",
	})
	if app["KAFKA_BOOTSTRAP_SERVERS"] != "leader.kafka-willow-bbbbbb.discoverd:9092" {
		t.Fatalf("bootstrap %q", app["KAFKA_BOOTSTRAP_SERVERS"])
	}
	if app["KAFKA_TRUSTED_CERT"] != "follower-ca" {
		t.Fatalf("tls ca %q", app["KAFKA_TRUSTED_CERT"])
	}
	if app["KAFKA_SASL_USERNAME"] != "flynn-admin" || app["KAFKA_SASL_PASSWORD"] != "super-secret" {
		t.Fatalf("sasl user=%q pass=%q", app["KAFKA_SASL_USERNAME"], app["KAFKA_SASL_PASSWORD"])
	}
}

func TestPluginInterpClusterCreateNameIsNotResource(t *testing.T) {
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		"pipeline": {ID: "pipeline-release"},
	}}
	in, rel, err := pluginInterp(client, &plugin.CLI{Command: "pipeline", App: "pipeline"}, &ct.Release{
		Env: map[string]string{"DATABASE_URL": "postgres://leader.postgres.discoverd/db"},
	}, "smoke-pipe")
	if err != nil {
		t.Fatal(err)
	}
	if in.Resource != "pipeline" || rel.ID != "pipeline-release" {
		t.Fatalf("pipeline:create <name> must use the plugin app, got resource %q release %v", in.Resource, rel)
	}
}

func TestCompactPluginArgsDropsEmptyURL(t *testing.T) {
	got := compactPluginArgs([]string{"psql", "", "-tAc", "SELECT 1"})
	if strings.Join(got, " ") != "psql -tAc SELECT 1" {
		t.Fatalf("%q", got)
	}
}

func TestCopyAppPGEnvFillsPlatformConsole(t *testing.T) {
	env := map[string]string{"PAGER": "less"}
	copyAppPGEnv(env, map[string]string{
		"PGHOST":     "leader.postgres.discoverd",
		"PGUSER":     "blob",
		"PGPASSWORD": "pw",
		"PGDATABASE": "blobdb",
		"PGSSLMODE":  "require",
	})
	if env["PGHOST"] != "leader.postgres.discoverd" || env["PGDATABASE"] != "blobdb" || env["PAGER"] != "less" {
		t.Fatalf("%v", env)
	}
}

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
