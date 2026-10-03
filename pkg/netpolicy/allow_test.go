package netpolicy

import (
	"strings"
	"testing"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestDiscoverdServicesFromEnv(t *testing.T) {
	path := t.TempDir() + "/installed-plugins.json"
	t.Setenv("FLYNN_INSTALLED_PLUGINS", path)
	if err := plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "redis", Datastore: true},
	}); err != nil {
		t.Fatal(err)
	}

	got := DiscoverdServicesFromEnv(map[string]string{
		"REDIS_URL":    "rediss://:s3cret@leader.redis-lagoon-59415.discoverd:6379",
		"FLYNN_REDIS":  "redis-lagoon-59415",
		"DATABASE_URL": "postgres://u:p@leader.postgresql-concave-48291.discoverd:5432/db",
		"CACHE_URL":    "rediss://cache.example.com:6379",
		"WEB_URL":      "http://app-one-web.discoverd",
		"CONTROLLER":   "http://controller.discoverd",
		"EXTRA":        "talk to leader.mysql-harbor-kxmnpq.discoverd please",
	})
	want := []string{"mysql-harbor-kxmnpq", "postgresql-concave-48291", "redis-lagoon-59415"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}

	if DiscoverdServicesFromEnv(map[string]string{"FOO": "bar"}) != nil {
		t.Fatal("no datastore URLs must yield no services")
	}
	if DiscoverdServicesFromEnv(nil) != nil {
		t.Fatal("nil env")
	}
}

func TestOverlayInstanceMetaAllowlist(t *testing.T) {
	user := &host.Job{
		ID: "node1-abc",
		Metadata: map[string]string{
			"flynn-controller.app_name": "app-one",
			"flynn-controller.type":     "worker",
		},
		Config: host.ContainerConfig{
			Env: map[string]string{
				"REDIS_URL": "rediss://:x@leader.redis-lagoon-59415.discoverd:6379",
			},
		},
	}
	meta := OverlayInstanceMeta(user)
	if meta["class"] != "user" || meta["job.id"] != "node1-abc" {
		t.Fatalf("meta=%v", meta)
	}
	if meta[MetaDiscoverdAllow] != "redis-lagoon-59415" {
		t.Fatalf("allow=%q", meta[MetaDiscoverdAllow])
	}

	empty := OverlayInstanceMeta(&host.Job{
		ID: "node1-def",
		Metadata: map[string]string{
			"flynn-controller.app_name": "app-one",
			"flynn-controller.type":     "web",
		},
	})
	if _, ok := empty[MetaDiscoverdAllow]; !ok {
		t.Fatal("user jobs must publish an allowlist key even when empty")
	}
	if empty[MetaDiscoverdAllow] != "" {
		t.Fatalf("empty allow=%q", empty[MetaDiscoverdAllow])
	}

	redis := OverlayInstanceMeta(&host.Job{
		ID: "node1-redis",
		Metadata: map[string]string{
			"flynn-system-app":          "true",
			"flynn-controller.app_name": "redis-lagoon-59415",
			"flynn-controller.type":     "redis",
		},
	})
	if redis["class"] != "datastore" {
		t.Fatalf("redis class=%q", redis["class"])
	}
	if _, ok := redis[MetaDiscoverdAllow]; ok {
		t.Fatal("datastore jobs do not publish a user allowlist")
	}
}

func TestParseDiscoverdAllow(t *testing.T) {
	if _, present := ParseDiscoverdAllow(nil); present {
		t.Fatal("nil meta")
	}
	if _, present := ParseDiscoverdAllow(map[string]string{"class": "user"}); present {
		t.Fatal("legacy instance without key")
	}
	allowed, present := ParseDiscoverdAllow(map[string]string{MetaDiscoverdAllow: ""})
	if !present || allowed != nil {
		t.Fatalf("empty present=%v allowed=%v", present, allowed)
	}
	allowed, present = ParseDiscoverdAllow(map[string]string{MetaDiscoverdAllow: "redis-lagoon-59415, postgresql-concave-48291"})
	if !present || strings.Join(allowed, ",") != "redis-lagoon-59415,postgresql-concave-48291" {
		t.Fatalf("allowed=%v present=%v", allowed, present)
	}
}

func TestUserMayResolveAttached(t *testing.T) {
	if UserMayResolveAttached(true, "redis-lagoon-59415", nil) {
		t.Fatal("empty allowlist must deny")
	}
	if !UserMayResolveAttached(true, "redis-lagoon-59415", []string{"redis-lagoon-59415"}) {
		t.Fatal("attached redis leader must resolve")
	}
	if UserMayResolveAttached(true, "pg-ridge-ffpade", []string{"redis-lagoon-59415"}) {
		t.Fatal("another app's datastore must not resolve")
	}
	if UserMayResolveAttached(false, "redis-lagoon-59415", []string{"redis-lagoon-59415"}) {
		t.Fatal("non-leader names stay blocked")
	}
	if UserMayResolveAttached(true, "app-one-web", []string{"app-one-web"}) {
		t.Fatal("allowlist must not open non-datastore services")
	}
}
