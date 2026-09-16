package cache

import (
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
)

func TestAddrsInstancesAndLeader(t *testing.T) {
	a := &discoverd.Instance{ID: "a", Addr: "10.0.0.1:80"}
	b := &discoverd.Instance{ID: "b", Addr: "10.0.0.2:80"}
	d := &ServiceCache{
		instances: map[string]*discoverd.Instance{"a": a, "b": b},
		leader:    a,
	}
	addrs := d.Addrs()
	if len(addrs) != 2 {
		t.Fatalf("%v", addrs)
	}
	if len(d.Instances()) != 2 {
		t.Fatal(d.Instances())
	}
	if got := d.LeaderAddr(); len(got) != 1 || got[0] != "10.0.0.1:80" {
		t.Fatalf("%v", got)
	}
	if got := d.Leader(); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("%v", got)
	}

	empty := &ServiceCache{instances: map[string]*discoverd.Instance{}}
	if len(empty.LeaderAddr()) != 0 || len(empty.Leader()) != 0 {
		t.Fatal("empty leader")
	}
}
