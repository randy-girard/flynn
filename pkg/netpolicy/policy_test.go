package netpolicy

import (
	"testing"

	host "github.com/flynn/flynn/host/types"
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
	if UserMayResolveDiscoverd(true, "kafka-api") {
		t.Fatal("user jobs must not resolve leader.kafka-api.discoverd")
	}
	if UserMayResolveDiscoverd(true, "shop-web") {
		t.Fatal("user jobs must not resolve leader.<app>-web.discoverd")
	}
	if UserMayResolveDiscoverd(true, "postgres-api") {
		t.Fatal("user jobs must not resolve leader.postgres-api.discoverd")
	}
}

func TestServiceForClass(t *testing.T) {
	if ServiceForClass(ClassUser) != ServiceUser {
		t.Fatal(ServiceForClass(ClassUser))
	}
	if ServiceForClass(ClassDatastore) != ServiceData {
		t.Fatal(ServiceForClass(ClassDatastore))
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
