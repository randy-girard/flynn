package clickhouse

import (
	"strings"
	"testing"
)

func TestRenderConfig(t *testing.T) {
	p := NewProcess()
	p.ReplicaName = "replica-1"
	p.AdvertisedHost = "10.0.0.1"
	p.Password = "secret"
	p.KeeperServers = []KeeperServer{{ID: 1, Host: "10.0.0.1", Port: DefaultKeeperRaftPort}}
	p.Replicas = []Replica{{Host: "10.0.0.1", Port: DefaultNativePort}}

	out, err := p.RenderConfig()
	if err != nil {
		t.Fatal(err)
	}

	mustContain(t, out, "<cluster>flynn</cluster>")
	mustContain(t, out, "<replica>replica-1</replica>")
	mustContain(t, out, "<internal_replication>true</internal_replication>")
	mustContain(t, out, "<password>secret</password>")
	mustContain(t, out, "<profiles>")
	mustContain(t, out, "<quotas>")
	mustContain(t, out, "<host>10.0.0.1</host>")
}

func TestRenderKeeperConfig(t *testing.T) {
	p := NewKeeperProcess()
	p.ServerID = 2
	p.Servers = []KeeperServer{
		{ID: 1, Host: "10.0.0.1", Port: DefaultKeeperRaftPort},
		{ID: 2, Host: "10.0.0.2", Port: DefaultKeeperRaftPort},
	}

	out, err := p.RenderKeeperConfig()
	if err != nil {
		t.Fatal(err)
	}

	mustContain(t, out, "<server_id>2</server_id>")
	mustContain(t, out, "<hostname>10.0.0.1</hostname>")
	mustContain(t, out, "<hostname>10.0.0.2</hostname>")
}

func TestBuildKeeperServers(t *testing.T) {
	got := BuildKeeperServers(map[int]string{
		30: "10.0.0.3:9234",
		10: "10.0.0.1:9234",
		20: "10.0.0.2:9234",
	}, DefaultKeeperRaftPort)
	if len(got) != 3 || got[0].ID != 10 || got[2].ID != 30 {
		t.Fatalf("unexpected keeper servers: %+v", got)
	}
}

func TestBuildReplicas(t *testing.T) {
	got := BuildReplicas(map[int]string{
		30: "10.0.0.3:9000",
		10: "10.0.0.1:9000",
	}, DefaultNativePort)
	if len(got) != 2 || got[0].Host != "10.0.0.1" {
		t.Fatalf("unexpected replicas: %+v", got)
	}
}

func mustContain(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}
