package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/plugin"
	"github.com/flynn/go-docopt"
)

func redisPluginCLI() *plugin.CLI {
	return &plugin.CLI{
		Command:         "redis",
		Usage:           "manage redis databases",
		ResourceEnv:     "FLYNN_REDIS",
		ResourceMissing: "No redis server found. Provision one with `flynn resource add redis`",
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
	})
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
	})
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
	_, _, err := pluginInterp(fakeRedisReleaseClient{}, redisPluginCLI(), &ct.Release{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "No redis server found") {
		t.Fatalf("got %v", err)
	}

	_, _, err = pluginInterp(fakeRedisReleaseClient{}, &plugin.CLI{Command: "widget"}, &ct.Release{})
	if err == nil || !strings.Contains(err.Error(), "does not declare a resource") {
		t.Fatalf("got %v", err)
	}

	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{"widget": {ID: "rel"}}}
	in, rel, err := pluginInterp(client, &plugin.CLI{Command: "widget", App: "widget"}, &ct.Release{})
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
	err := executePluginCLI(nil, redisPluginCLI(), &docopt.Args{Bool: map[string]bool{}})
	if err == nil || !strings.Contains(err.Error(), "no matching plugin CLI action") {
		t.Fatalf("got %v", err)
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
	})
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

func containsArgPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
