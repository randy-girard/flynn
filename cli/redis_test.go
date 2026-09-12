package main

import (
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
	if strings.HasPrefix(got, app+".") {
		t.Fatal("must not dial the internal redis-<uuid>.discoverd name")
	}
}
