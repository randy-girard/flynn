package main

import (
	"fmt"
	"strings"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestRedisDialHost(t *testing.T) {
	app := "redis-11111111-2222-3333-4444-555555555555"
	got := redisDialHost(&ct.Release{Env: map[string]string{
		"REDIS_HOST": "leader." + app + ".discoverd",
	}}, app)
	if got != "leader."+app+".discoverd" {
		t.Fatalf("REDIS_HOST: got %q", got)
	}
	got = redisDialHost(&ct.Release{Env: map[string]string{}}, app)
	if got != "leader."+app+".discoverd" {
		t.Fatalf("fallback: got %q", got)
	}
	got = redisDialHost(nil, app)
	if got != "leader."+app+".discoverd" {
		t.Fatalf("nil release: got %q", got)
	}
	if strings.HasPrefix(got, app+".") {
		t.Fatal("must not dial the internal redis-<uuid>.discoverd name")
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

func TestGetRedisRunConfigDialsLeaderDiscoverd(t *testing.T) {
	redisApp := "redis-11111111-2222-3333-4444-555555555555"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		redisApp: {ID: "redis-release"},
	}}
	cfg, err := getRedisRunConfig(client, "shop", &ct.Release{
		Env: map[string]string{
			"FLYNN_REDIS":    redisApp,
			"REDIS_PASSWORD": "s3cret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantHost := "leader." + redisApp + ".discoverd"
	joined := strings.Join(cfg.Args, " ")
	if !containsArgPair(cfg.Args, "-h", wantHost) {
		t.Fatalf("redis-cli args %q must pass -h %s", joined, wantHost)
	}
	if strings.Contains(joined, " "+redisApp+".discoverd") {
		t.Fatalf("must not dial %s.discoverd: %q", redisApp, joined)
	}
	if cfg.Release != "redis-release" {
		t.Fatalf("Release=%q", cfg.Release)
	}
}

func TestGetRedisRunConfigUsesREDISHost(t *testing.T) {
	redisApp := "redis-11111111-2222-3333-4444-555555555555"
	client := fakeRedisReleaseClient{releases: map[string]*ct.Release{
		redisApp: {ID: "redis-release"},
	}}
	host := "leader." + redisApp + ".discoverd"
	cfg, err := getRedisRunConfig(client, "shop", &ct.Release{
		Env: map[string]string{
			"FLYNN_REDIS":    redisApp,
			"REDIS_HOST":     host,
			"REDIS_PASSWORD": "s3cret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsArgPair(cfg.Args, "-h", host) {
		t.Fatalf("args %q must pass provisioned REDIS_HOST", strings.Join(cfg.Args, " "))
	}
}

func TestGetRedisRunConfigRequiresResource(t *testing.T) {
	_, err := getRedisRunConfig(fakeRedisReleaseClient{}, "shop", &ct.Release{Env: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "No redis server found") {
		t.Fatalf("got %v", err)
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
