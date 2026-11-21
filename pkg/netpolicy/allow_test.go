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

// TestDiscoverdAllowIgnoresTenantSetEnv: a tenant controls release and
// one-off env, so `flynn env:set X=leader.redis-victim.discoverd` must not
// widen the DNS allowlist. Only the controller-stamped job metadata counts.
func TestDiscoverdAllowIgnoresTenantSetEnv(t *testing.T) {
	path := t.TempDir() + "/installed-plugins.json"
	t.Setenv("FLYNN_INSTALLED_PLUGINS", path)
	if err := plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "redis", Datastore: true},
	}); err != nil {
		t.Fatal(err)
	}
	tenantEnv := map[string]string{
		"X":           "leader.redis-victim-11111.discoverd",
		"REDIS_URL":   "redis://:x@leader.redis-victim-22222.discoverd:6379",
		"FLYNN_REDIS": "redis-victim-33333",
	}

	attached := OverlayInstanceMeta(&host.Job{
		ID: "node1-abc",
		Metadata: map[string]string{
			"flynn-controller.app_name": "app-one",
			"flynn-controller.type":     "web",
			MetaJobDiscoverdAllow:       "redis-lagoon-59415",
		},
		Config: host.ContainerConfig{Env: tenantEnv},
	})
	if got := attached[MetaDiscoverdAllow]; got != "redis-lagoon-59415" {
		t.Fatalf("allow=%q, want only the stamped attachment", got)
	}

	none := OverlayInstanceMeta(&host.Job{
		ID: "node1-def",
		Metadata: map[string]string{
			"flynn-controller.app_name": "app-one",
			"flynn-controller.type":     "web",
			MetaJobDiscoverdAllow:       "",
		},
		Config: host.ContainerConfig{Env: tenantEnv},
	})
	if v, ok := none[MetaDiscoverdAllow]; !ok || v != "" {
		t.Fatalf("allow=%q present=%v, want empty: env must not add services", v, ok)
	}

	// The stamp is filtered the same way: a non-datastore name never opens.
	filtered := OverlayInstanceMeta(&host.Job{
		ID: "node1-ghi",
		Metadata: map[string]string{
			"flynn-controller.app_name": "app-one",
			"flynn-controller.type":     "web",
			MetaJobDiscoverdAllow:       "app-two-web, Redis-Lagoon-59415,controller",
		},
	})
	if got := filtered[MetaDiscoverdAllow]; got != "redis-lagoon-59415" {
		t.Fatalf("allow=%q", got)
	}
}

// Jobs started by a controller from before the stamp existed keep running
// across a flynn-host update. They keep the env-derived list so their
// datastore still resolves; every job a current controller starts is stamped.
func TestDiscoverdAllowLegacyJobWithoutStampUsesEnv(t *testing.T) {
	path := t.TempDir() + "/installed-plugins.json"
	t.Setenv("FLYNN_INSTALLED_PLUGINS", path)
	if err := plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "redis", Datastore: true},
	}); err != nil {
		t.Fatal(err)
	}
	meta := OverlayInstanceMeta(&host.Job{
		ID: "node1-old",
		Metadata: map[string]string{
			"flynn-controller.app_name": "app-one",
			"flynn-controller.type":     "web",
		},
		Config: host.ContainerConfig{Env: map[string]string{
			"REDIS_URL": "redis://:x@leader.redis-lagoon-59415.discoverd:6379",
		}},
	})
	if got := meta[MetaDiscoverdAllow]; got != "redis-lagoon-59415" {
		t.Fatalf("allow=%q", got)
	}
}

func TestAttachedDiscoverdServices(t *testing.T) {
	path := t.TempDir() + "/installed-plugins.json"
	t.Setenv("FLYNN_INSTALLED_PLUGINS", path)
	if err := plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "redis", Datastore: true},
	}); err != nil {
		t.Fatal(err)
	}
	got := AttachedDiscoverdServices([]map[string]string{
		{"FLYNN_REDIS": "redis-lagoon-59415", "REDIS_URL": "redis://:x@leader.redis-lagoon-59415.discoverd:6379"},
		{"DATABASE_URL": "postgres://u:p@leader.postgresql-concave-48291.discoverd:5432/db"},
		nil,
		{"FOO": "bar"},
	})
	if strings.Join(got, ",") != "postgresql-concave-48291,redis-lagoon-59415" {
		t.Fatalf("got %v", got)
	}
	// The controller has no installed-plugins.json: shared appliance names
	// are kept here and filtered on the host.
	t.Setenv("FLYNN_INSTALLED_PLUGINS", t.TempDir()+"/missing.json")
	if got := AttachedDiscoverdServices([]map[string]string{{"FLYNN_MYSQL": "MariaDB"}}); strings.Join(got, ",") != "mariadb" {
		t.Fatalf("got %v", got)
	}
	if got := AttachedDiscoverdServices(nil); got == nil || len(got) != 0 {
		t.Fatalf("no resources must give an empty, non-nil list, got %#v", got)
	}
	job := &host.Job{}
	StampDiscoverdAllow(job, []string{"redis-a", "pg-b"})
	if job.Metadata[MetaJobDiscoverdAllow] != "redis-a,pg-b" {
		t.Fatalf("stamp=%q", job.Metadata[MetaJobDiscoverdAllow])
	}
	StampDiscoverdAllow(job, nil)
	if v, ok := job.Metadata[MetaJobDiscoverdAllow]; !ok || v != "" {
		t.Fatalf("empty stamp=%q ok=%v", v, ok)
	}
}
