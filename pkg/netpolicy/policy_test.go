package netpolicy

import (
	"path/filepath"
	"testing"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestClassifyJob(t *testing.T) {
	cases := []struct {
		name string
		job  *host.Job
		want Class
	}{
		{name: "nil", job: nil, want: ClassUser},
		{
			name: "user web",
			job:  &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}},
			want: ClassUser,
		},
		{
			name: "user one-off",
			job:  &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "run"}},
			want: ClassUser,
		},
		{
			name: "dockerbuilder is build not user",
			job: &host.Job{Metadata: map[string]string{
				"flynn-controller.app_name": "shop",
				"flynn-controller.type":     "dockerbuilder",
			}},
			want: ClassBuild,
		},
		{
			name: "slugbuilder is build",
			job:  &host.Job{Metadata: map[string]string{"flynn-controller.type": "slugbuilder"}},
			want: ClassBuild,
		},
		{
			name: "postgres data plane",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "postgres",
				"flynn-controller.type":     "postgres",
			}},
			want: ClassDatastore,
		},
		{
			name: "postgres-api is system not datastore",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "postgres",
				"flynn-controller.type":     "web",
			}},
			want: ClassSystem,
		},
		{
			name: "kafka appliance",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "kafka-11111111-2222-3333-4444-555555555555",
				"flynn-controller.type":     "kafka",
			}},
			want: ClassDatastore,
		},
		{
			name: "clickhouse appliance",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "clickhouse-11111111-2222-3333-4444-555555555555",
				"flynn-controller.type":     "clickhouse",
			}},
			want: ClassDatastore,
		},
		{
			name: "redis appliance",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "redis-11111111-2222-3333-4444-555555555555",
				"flynn-controller.type":     "redis",
			}},
			want: ClassDatastore,
		},
		{
			name: "mariadb data plane",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-datastore":           "true",
				"flynn-controller.app_name": "mariadb",
				"flynn-controller.type":     "mariadb",
			}},
			want: ClassDatastore,
		},
		{
			name: "mongodb data plane",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-datastore":           "true",
				"flynn-controller.app_name": "mongodb",
				"flynn-controller.type":     "mongodb",
			}},
			want: ClassDatastore,
		},
		{
			name: "redis-api is system not datastore",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "redis",
				"flynn-controller.type":     "web",
			}},
			want: ClassSystem,
		},
		{
			name: "redis-api app name is not a uuid appliance",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "redis-api",
				"flynn-controller.type":     "web",
			}},
			want: ClassSystem,
		},
		{
			name: "mariadb-api is system not datastore",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "mariadb",
				"flynn-controller.type":     "web",
			}},
			want: ClassSystem,
		},
		{
			name: "kafka-api is system not datastore",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "kafka-api",
				"flynn-controller.type":     "web",
			}},
			want: ClassSystem,
		},
		{
			name: "blobstore",
			job: &host.Job{Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-controller.app_name": "blobstore",
				"flynn-controller.type":     "app",
			}},
			want: ClassSystem,
		},
		{
			name: "system partition without meta",
			job:  &host.Job{Partition: "system"},
			want: ClassSystem,
		},
		{
			name: "maintainer builder",
			job:  &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "builder"}},
			want: ClassSystem,
		},
	}
	for _, tc := range cases {
		if got := ClassifyJob(tc.job); got != tc.want {
			t.Errorf("%s: ClassifyJob=%s want %s", tc.name, got, tc.want)
		}
	}
}

func TestUserMayResolveDiscoverd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed-plugins.json")
	t.Setenv("FLYNN_INSTALLED_PLUGINS", path)
	if err := plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "mariadb", Datastore: true},
		{Name: "mongodb", Datastore: true},
		{Name: "redis", Datastore: true},
	}); err != nil {
		t.Fatal(err)
	}
	if UserMayResolveDiscoverd(false, "postgres") {
		t.Fatal("user jobs must not resolve internal discoverd names")
	}
	if !UserMayResolveDiscoverd(true, "postgres") {
		t.Fatal("user jobs may resolve leader.postgres.discoverd")
	}
	if !UserMayResolveDiscoverd(true, "redis-11111111-2222-3333-4444-555555555555") {
		t.Fatal("user jobs may resolve leader.redis-<uuid>.discoverd")
	}
	if !UserMayResolveDiscoverd(true, "kafka-11111111-2222-3333-4444-555555555555") {
		t.Fatal("user jobs may resolve leader.kafka-<uuid>.discoverd")
	}
	if !UserMayResolveDiscoverd(true, "clickhouse-11111111-2222-3333-4444-555555555555") {
		t.Fatal("user jobs may resolve leader.clickhouse-<uuid>.discoverd")
	}
	if !UserMayResolveDiscoverd(true, "mariadb") {
		t.Fatal("user jobs may resolve leader.mariadb.discoverd")
	}
	if !UserMayResolveDiscoverd(true, "mongodb") {
		t.Fatal("user jobs may resolve leader.mongodb.discoverd")
	}
	if !UserMayResolveDiscoverd(true, "redis") {
		t.Fatal("user jobs may resolve leader.redis.discoverd")
	}
	if UserMayResolveDiscoverd(false, "mariadb") {
		t.Fatal("user jobs must not resolve mariadb.discoverd (non-leader)")
	}
	if UserMayResolveDiscoverd(true, "kafka-api") {
		t.Fatal("user jobs must not resolve leader.kafka-api.discoverd")
	}
	if UserMayResolveDiscoverd(true, "shop-web") {
		t.Fatal("user jobs must not resolve leader.<app>-web.discoverd")
	}
	if UserMayResolveDiscoverd(true, "postgres-api") {
		t.Fatal("user jobs must not resolve leader.postgres-api.discoverd")
	}
	if UserMayResolveDiscoverd(true, "redis-api") {
		t.Fatal("user jobs must not resolve leader.redis-api.discoverd")
	}
	if UserMayResolveDiscoverd(true, "mongodb-api") {
		t.Fatal("user jobs must not resolve leader.mongodb-api.discoverd")
	}
	if UserMayResolveDiscoverd(true, "redis-GGGGGGGG-728c-4eb5-8c1d-a0d38924cbd8") {
		t.Fatal("non-hex UUID appliance names must be denied")
	}
	if UserMayResolveDiscoverd(true, "redis-621e38ec728c4eb58c1da0d38924cbd8") {
		t.Fatal("UUID appliance names without dashes must be denied")
	}
}

func TestServiceForClass(t *testing.T) {
	cases := []struct {
		c    Class
		svc  string
		name string
	}{
		{ClassUser, ServiceUser, "user"},
		{ClassBuild, ServiceBuild, "build"},
		{ClassDatastore, ServiceData, "datastore"},
		{ClassSystem, ServiceSys, "system"},
		{Class(99), ServiceUser, "user"},
	}
	for _, tc := range cases {
		if ServiceForClass(tc.c) != tc.svc || tc.c.String() != tc.name {
			t.Fatalf("%v -> %s %q", tc.c, ServiceForClass(tc.c), tc.c.String())
		}
	}
}

func TestInstanceHost(t *testing.T) {
	if ip := InstanceHost("100.64.82.14:1"); ip == nil || ip.String() != "100.64.82.14" {
		t.Fatalf("host:port = %v", ip)
	}
	if ip := InstanceHost("100.64.82.14"); ip == nil || ip.String() != "100.64.82.14" {
		t.Fatalf("bare IP = %v", ip)
	}
	if InstanceHost("") != nil {
		t.Fatal("empty addr")
	}
}
