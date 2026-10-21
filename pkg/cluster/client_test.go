package cluster

import (
	"net/http"
	"reflect"
	"testing"
)

func TestUniqueHostsDedupsDuplicateIDs(t *testing.T) {
	tcp := NewHost("devnode1", "192.168.57.20:1113", http.DefaultClient, nil)
	httpHost := NewHost("devnode1", "http://192.168.57.20:1113", http.DefaultClient, nil)
	other := NewHost("devnode2", "http://192.168.57.21:1113", http.DefaultClient, nil)
	got := UniqueHosts([]*Host{tcp, httpHost, other, tcp})
	if len(got) != 2 {
		t.Fatalf("got %d hosts, want 2", len(got))
	}
	ids := []string{got[0].ID(), got[1].ID()}
	if !reflect.DeepEqual(ids, []string{"devnode1", "devnode2"}) {
		t.Fatalf("ids=%v", ids)
	}
}

func TestUniqueHostsPrefersHostAPIPort(t *testing.T) {
	otherPort := NewHost("devnode1", "192.168.57.20:1111", http.DefaultClient, nil)
	api := NewHost("devnode1", "192.168.57.20:1113", http.DefaultClient, nil)
	got := UniqueHosts([]*Host{otherPort, api})
	if len(got) != 1 || got[0].Addr() != "192.168.57.20:1113" {
		t.Fatalf("got %+v", got[0].Addr())
	}
}

func TestIsHostAPIAddr(t *testing.T) {
	if !isHostAPIAddr("192.168.57.20:1113") || !isHostAPIAddr("http://192.168.57.20:1113/host") {
		t.Fatal("expected :1113 to match")
	}
	if isHostAPIAddr("192.168.57.20:1111") || isHostAPIAddr("192.168.57.20:80") {
		t.Fatal("non-API ports must not match")
	}
}
