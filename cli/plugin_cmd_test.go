package main

import (
	"fmt"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/plugin"
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
