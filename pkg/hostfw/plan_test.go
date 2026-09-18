package hostfw

import (
	"reflect"
	"testing"

	router "github.com/randy-girard/flynn/router/types"
)

func TestParsePeerIP(t *testing.T) {
	ip, err := ParsePeerIP(" 192.168.56.21 ")
	if err != nil || ip != "192.168.56.21" {
		t.Fatalf("bare: %q %v", ip, err)
	}
	ip, err = ParsePeerIP("10.0.0.5:1113")
	if err != nil || ip != "10.0.0.5" {
		t.Fatalf("hostport: %q %v", ip, err)
	}
	if _, err := ParsePeerIP(""); err == nil {
		t.Fatal("empty IP must fail")
	}
	if _, err := ParsePeerIP("not-an-ip"); err == nil {
		t.Fatal("hostname must fail")
	}
}

func TestNormalizePeersDropsSelfAndDupes(t *testing.T) {
	got := NormalizePeers([]string{"192.168.56.21", "192.168.56.21:1113", "192.168.56.20", "bad"}, "192.168.56.20")
	want := []string{"192.168.56.21"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTCPPortsFromRoutes(t *testing.T) {
	got := TCPPortsFromRoutes([]*router.Route{
		{Type: "http", Port: 443},
		{Type: "tcp", Port: 3001},
		{Type: "tcp", Port: 80},
		{Type: "tcp", Port: 3001},
		{Type: "tcp", Port: 0},
		nil,
	})
	want := []int{3001}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPlanAndDiffPeerAddRemove(t *testing.T) {
	want := Plan(Desired{PeerIPs: []string{"192.168.56.21", "192.168.56.22"}, SelfIP: "192.168.56.20"})
	if len(want) != 2 {
		t.Fatalf("plan: %+v", want)
	}
	have := []Rule{want[0]}
	add, remove := Diff(have, want)
	if len(add) != 1 || add[0].From != "192.168.56.22" || len(remove) != 0 {
		t.Fatalf("add=%+v remove=%+v", add, remove)
	}
	add, remove = Diff(want, have)
	if len(remove) != 1 || remove[0].From != "192.168.56.22" || len(add) != 0 {
		t.Fatalf("remove add=%+v remove=%+v", add, remove)
	}
}

func TestPlanAndDiffExposePorts(t *testing.T) {
	want := Plan(Desired{ExposedTCP: []int{3001, 22, 443, 3002}})
	if len(want) != 2 || want[0].Port != 3001 || want[1].Port != 3002 {
		t.Fatalf("plan %+v", want)
	}
	add, remove := Diff(nil, want)
	if len(add) != 2 || len(remove) != 0 {
		t.Fatalf("add=%+v remove=%+v", add, remove)
	}
	add, remove = Diff(want, Plan(Desired{ExposedTCP: []int{3001}}))
	if len(add) != 0 || len(remove) != 1 || remove[0].Port != 3002 {
		t.Fatalf("close port add=%+v remove=%+v", add, remove)
	}
}

func TestDiffIgnoresInstallerOwnedRules(t *testing.T) {
	have := []Rule{
		{Kind: KindPublic, Port: 22, Comment: CommentPublic},
		{Kind: KindCluster, From: "10.0.0.0/8", Comment: CommentCluster},
		{Kind: KindPeer, From: "1.2.3.4", Comment: CommentPeer},
	}
	want := Plan(Desired{PeerIPs: []string{"1.2.3.4", "5.6.7.8"}})
	add, remove := Diff(have, want)
	if len(remove) != 0 {
		t.Fatalf("must not remove installer rules: %+v", remove)
	}
	if len(add) != 1 || add[0].From != "5.6.7.8" {
		t.Fatalf("add %+v", add)
	}
}
