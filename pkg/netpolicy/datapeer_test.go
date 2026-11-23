package netpolicy

import (
	"net"
	"sort"
	"strings"
	"testing"

	host "github.com/randy-girard/flynn/host/types"
)

// TestDatastoreAppOneOffJobsAreNotEngines (EDGE-29): the datastore network
// class only allows peer, DNS and discoverd traffic, so it must hold only
// the formation engines. One-off jobs in a datastore app (the cluster
// backup's pg_dumpall, plugin `maintenance` and backup tasks) are platform
// clients of the controller and blobstore: they are system class on the
// network, but still not host-trusted.
func TestDatastoreAppOneOffJobsAreNotEngines(t *testing.T) {
	meta := func(app, typ string, extra ...string) map[string]string {
		m := map[string]string{
			"flynn-system-app":          "true",
			"flynn-controller.app":      "11111111-2222-3333-4444-555555555555",
			"flynn-controller.app_name": app,
			"flynn-controller.type":     typ,
		}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	for _, tc := range []struct {
		name string
		job  *host.Job
		want Class
	}{
		{"postgres formation engine", &host.Job{Partition: "system", Metadata: meta("postgres", "postgres", "flynn-controller.formation", "true")}, ClassDatastore},
		{"plugin instance engine", &host.Job{Metadata: meta("redis-lagoon-59415", "redis", "flynn-controller.formation", "true", "flynn-datastore", "true")}, ClassDatastore},
		{"cluster backup pg_dumpall (runner)", &host.Job{Partition: "background", Metadata: meta("postgres", "runner")}, ClassSystem},
		{"plugin owned maintenance (console)", &host.Job{Partition: "system", Metadata: meta("postgres-plugin", "console", "flynn-datastore", "true")}, ClassSystem},
		{"controller one-off with explicit type", &host.Job{Partition: "system", Metadata: meta("redis-plugin", "backup", "flynn-datastore", "true")}, ClassSystem},
		{"engine without controller metadata (bootstrap fixture)", &host.Job{Partition: "system", Metadata: map[string]string{"flynn-system-app": "true", "flynn-controller.app_name": "postgres", "flynn-controller.type": "postgres"}}, ClassDatastore},
	} {
		if got := ClassifyJob(tc.job); got != tc.want {
			t.Errorf("%s: class %s, want %s", tc.name, got, tc.want)
		}
		if HostTrusted(tc.job) {
			t.Errorf("%s: a job in a datastore app other than web must not be host-trusted", tc.name)
		}
	}
}

// TestDatastoreInstanceMetaNamesItsApp: flynn-host publishes the app of each
// datastore engine so every host can pair engines of the same app.
func TestDatastoreInstanceMetaNamesItsApp(t *testing.T) {
	job := &host.Job{ID: "node1-x", Metadata: map[string]string{
		"flynn-system-app":           "true",
		"flynn-datastore":            "true",
		"flynn-controller.app":       "aaaa",
		"flynn-controller.app_name":  "redis-lagoon-59415",
		"flynn-controller.type":      "redis",
		"flynn-controller.formation": "true",
	}}
	if got := OverlayInstanceMeta(job)[MetaDatastoreApp]; got != "aaaa" {
		t.Fatalf("%s = %q, want the app id", MetaDatastoreApp, got)
	}
	delete(job.Metadata, "flynn-controller.app")
	if got := OverlayInstanceMeta(job)[MetaDatastoreApp]; got != "redis-lagoon-59415" {
		t.Fatalf("%s = %q, want the app name when there is no id", MetaDatastoreApp, got)
	}
	if _, ok := OverlayInstanceMeta(job)[MetaDatastoreSystem]; ok {
		t.Fatal("a plugin instance is not a platform datastore")
	}
	pg := &host.Job{Partition: "system", Metadata: map[string]string{
		"flynn-system-app":           "true",
		"flynn-controller.app":       "pgid",
		"flynn-controller.app_name":  "postgres",
		"flynn-controller.type":      "postgres",
		"flynn-controller.formation": "true",
	}}
	if got := OverlayInstanceMeta(pg)[MetaDatastoreSystem]; got != "true" {
		t.Fatalf("system postgres %s = %q", MetaDatastoreSystem, got)
	}
	if !DatastoreSystemFromMeta(map[string]string{MetaDatastoreSystem: "true"}) || DatastoreSystemFromMeta(map[string]string{}) {
		t.Fatal("DatastoreSystemFromMeta")
	}
	sys := &host.Job{Metadata: map[string]string{"flynn-system-app": "true", "flynn-controller.app": "c", "flynn-controller.app_name": "controller", "flynn-controller.type": "web"}}
	if _, ok := OverlayInstanceMeta(sys)[MetaDatastoreApp]; ok {
		t.Fatal("only datastore engines publish their app")
	}
	if app, ok := DatastoreAppFromMeta(map[string]string{MetaDatastoreApp: "aaaa"}); !ok || app != "aaaa" {
		t.Fatalf("DatastoreAppFromMeta = %q %v", app, ok)
	}
	if _, ok := DatastoreAppFromMeta(map[string]string{"class": "datastore"}); ok {
		t.Fatal("a registration without the key is a legacy one")
	}
}

func pairStrings(pairs [][2]net.IP) []string {
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p[0].String()+">"+p[1].String())
	}
	sort.Strings(out)
	return out
}

// TestDatastorePeerPairs: engines of the same app are paired both ways;
// engines of different apps are not. A registration from a flynn-host that
// predates the app key (mid rolling update) is paired with every engine, so
// clustering keeps working until that host re-registers it.
func TestDatastorePeerPairs(t *testing.T) {
	ip := net.ParseIP
	got := pairStrings(DatastorePeerPairs([]DatastorePeer{
		{IP: ip("100.64.1.1"), App: "kafka-a", Known: true},
		{IP: ip("100.64.2.1"), App: "kafka-a", Known: true},
		{IP: ip("100.64.1.1"), App: "kafka-a", Known: true}, // duplicate (local + discoverd)
		{IP: ip("100.64.3.1"), App: "clickhouse-b", Known: true},
		{IP: nil, App: "x", Known: true},
	}))
	want := []string{"100.64.1.1>100.64.2.1", "100.64.2.1>100.64.1.1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("pairs = %v, want %v", got, want)
	}

	got = pairStrings(DatastorePeerPairs([]DatastorePeer{
		{IP: ip("100.64.1.1"), App: "kafka-a", Known: true},
		{IP: ip("100.64.3.1"), App: "clickhouse-b", Known: true},
		{IP: ip("100.64.4.1")}, // legacy registration
	}))
	want = []string{"100.64.1.1>100.64.4.1", "100.64.3.1>100.64.4.1", "100.64.4.1>100.64.1.1", "100.64.4.1>100.64.3.1"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("legacy pairs = %v, want %v", got, want)
	}
}

// TestDatastorePeerTCPPortsAreAuthenticated: the ports any engine may open
// on any other engine are the ones that need a password, client
// certificate or key. Unauthenticated cluster-internal listeners stay
// same-app only.
func TestDatastorePeerTCPPortsAreAuthenticated(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DatastorePeerTCPPorts {
		seen[p] = true
	}
	for _, p := range []string{"5432", "5433", "3306", "3307", "6379", "16379", "27017"} {
		if !seen[p] {
			t.Errorf("cross-app replication port %s missing", p)
		}
	}
	for _, p := range []string{"9009", "9181", "9234", "9093", "9094", "9180"} {
		if seen[p] {
			t.Errorf("unauthenticated port %s must stay same-app only", p)
		}
	}
}

// TestStripDatastoreEscapeCoversOneOffJobs: moving one-off jobs in a
// datastore app to the system network class must not let them keep host
// networking or capabilities.
func TestStripDatastoreEscapeCoversOneOffJobs(t *testing.T) {
	job := &host.Job{Partition: "background", Metadata: map[string]string{
		"flynn-system-app":          "true",
		"flynn-controller.app":      "pg",
		"flynn-controller.app_name": "postgres",
		"flynn-controller.type":     "runner",
	}, Config: host.ContainerConfig{HostNetwork: true}}
	StripDatastoreEscape(job)
	if job.Config.HostNetwork {
		t.Fatal("one-off job in a datastore app kept host networking")
	}
}

func TestDatastoreAppJob(t *testing.T) {
	m := func(typ string, extra ...string) *host.Job {
		md := map[string]string{"flynn-system-app": "true", "flynn-controller.app": "pg", "flynn-controller.app_name": "postgres", "flynn-controller.type": typ}
		for i := 0; i+1 < len(extra); i += 2 {
			md[extra[i]] = extra[i+1]
		}
		return &host.Job{Metadata: md}
	}
	if !DatastoreAppJob(m("postgres", "flynn-controller.formation", "true")) || !DatastoreAppJob(m("runner")) {
		t.Fatal("engines and one-offs in a datastore app are datastore-app jobs")
	}
	if DatastoreAppJob(m("web", "flynn-controller.formation", "true")) {
		t.Fatal("the web API is not")
	}
	if DatastoreAppJob(&host.Job{Metadata: map[string]string{"flynn-controller.app_name": "postgres", "flynn-controller.type": "postgres"}}) {
		t.Fatal("a user app named postgres is not")
	}
}

// TestSystemDatastoreIPs: the platform Postgres engines, which only their
// own peers may open.
func TestSystemDatastoreIPs(t *testing.T) {
	ip := net.ParseIP
	got := SystemDatastoreIPs([]DatastorePeer{
		{IP: ip("100.64.1.1"), App: "pg", Known: true, System: true},
		{IP: ip("100.64.1.1"), App: "pg", Known: true, System: true},
		{IP: ip("100.64.2.1"), App: "redis-a", Known: true},
		{IP: nil, System: true},
	})
	if len(got) != 1 || got[0].String() != "100.64.1.1" {
		t.Fatalf("got %v", got)
	}
}
