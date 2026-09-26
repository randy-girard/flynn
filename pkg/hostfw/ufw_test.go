package hostfw

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/instanceport"
)

type fakeUFW struct {
	rules []Rule
	log   []string
}

func (f *fakeUFW) List() ([]Rule, error) { return append([]Rule{}, f.rules...), nil }

func (f *fakeUFW) Allow(r Rule) error {
	f.log = append(f.log, "allow "+r.Key())
	f.rules = append(f.rules, r)
	return nil
}

func (f *fakeUFW) Delete(r Rule) error {
	f.log = append(f.log, "delete "+r.Key())
	var rest []Rule
	for _, have := range f.rules {
		if have.Key() != r.Key() {
			rest = append(rest, have)
		}
	}
	f.rules = rest
	return nil
}

func TestParseUFWStatus(t *testing.T) {
	out := `
Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere                   # flynn-public
22/tcp (v6)                ALLOW       Anywhere (v6)              # flynn-public
Anywhere                   ALLOW       10.0.0.0/8                 # flynn-cluster
Anywhere                   ALLOW       192.168.56.21              # flynn-peer
3001/tcp                   ALLOW       Anywhere                   # flynn-expose
3100/tcp                   ALLOW       Anywhere                   # flynn-instance
80/tcp                     ALLOW       Anywhere
`
	got := ParseUFWStatus(out)
	if len(got) != 5 {
		t.Fatalf("got %+v", got)
	}
	kinds := map[string]int{}
	for _, r := range got {
		kinds[r.Kind]++
	}
	if kinds[KindPublic] != 1 || kinds[KindCluster] != 1 || kinds[KindPeer] != 1 || kinds[KindExpose] != 1 || kinds[KindInstance] != 1 {
		t.Fatalf("kinds %+v rules %+v", kinds, got)
	}
}

func TestReconcileInstancePortsFollowsHostPlan(t *testing.T) {
	jobs := []instanceport.Job{
		{InstanceID: "a", Port: 3100, Host: "h1"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	}
	b := &fakeUFW{rules: []Rule{
		{Kind: KindPeer, From: "10.0.0.2", Comment: CommentPeer},
		{Kind: KindExpose, Port: 3001, Comment: CommentExpose},
	}}
	if err := ReconcileInstancePorts(b, InstancePorts("h1", jobs)); err != nil {
		t.Fatal(err)
	}
	if ports := instancePorts(b.rules); !reflect.DeepEqual(ports, []int{3100}) {
		t.Fatalf("h1 opened %v rules %+v", ports, b.rules)
	}
	if err := Reconcile(b, Desired{PeerIPs: []string{"10.0.0.2"}, ExposedTCP: []int{3001}}); err != nil {
		t.Fatal(err)
	}
	if ports := instancePorts(b.rules); !reflect.DeepEqual(ports, []int{3100}) {
		t.Fatalf("route sync closed instance port: %+v", b.rules)
	}

	moved := []instanceport.Job{
		{InstanceID: "a", Port: 3100, Host: "h2"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	}
	if err := ReconcileInstancePorts(b, InstancePorts("h1", moved)); err != nil {
		t.Fatal(err)
	}
	if ports := instancePorts(b.rules); len(ports) != 0 {
		t.Fatalf("old host still open %v", ports)
	}
	if err := ReconcileInstancePorts(b, InstancePorts("h2", moved)); err != nil {
		t.Fatal(err)
	}
	if ports := instancePorts(b.rules); !reflect.DeepEqual(ports, []int{3100, 3101}) {
		t.Fatalf("new host %v", ports)
	}
	for _, r := range InstanceRules(InstancePorts("h1", jobs)) {
		if r.Port == 3101 {
			t.Fatal("plan for the host running A included B's port")
		}
	}
}

func instancePorts(rules []Rule) []int {
	var ports []int
	for _, r := range InstanceManaged(rules) {
		ports = append(ports, r.Port)
	}
	return ports
}

func TestReconcileAddAndRemove(t *testing.T) {
	b := &fakeUFW{rules: []Rule{
		{Kind: KindPublic, Port: 22, Comment: CommentPublic},
		{Kind: KindPeer, From: "10.0.0.2", Comment: CommentPeer},
		{Kind: KindExpose, Port: 3009, Comment: CommentExpose},
	}}
	if err := Reconcile(b, Desired{
		PeerIPs:    []string{"10.0.0.3"},
		ExposedTCP: []int{3001},
		SelfIP:     "10.0.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	if !containsLog(b.log, "delete peer|0|10.0.0.2") || !containsLog(b.log, "delete expose|3009|") {
		t.Fatalf("expected deletes, log=%q", b.log)
	}
	if !containsLog(b.log, "allow peer|0|10.0.0.3") || !containsLog(b.log, "allow expose|3001|") {
		t.Fatalf("expected allows, log=%q", b.log)
	}
	for _, r := range b.rules {
		if r.Kind == KindPublic {
			return
		}
	}
	t.Fatal("installer public rule was dropped")
}

func containsLog(log []string, needle string) bool {
	for _, s := range log {
		if s == needle {
			return true
		}
	}
	return false
}

func TestUFWAllowArgs(t *testing.T) {
	got := strings.Join(ufwAllowArgs(Rule{Kind: KindPeer, From: "1.2.3.4", Comment: CommentPeer}), " ")
	if got != "allow from 1.2.3.4 comment flynn-peer" {
		t.Fatalf("peer args %q", got)
	}
	got = strings.Join(ufwAllowArgs(Rule{Kind: KindExpose, Port: 3001, Comment: CommentExpose}), " ")
	if got != "allow 3001/tcp comment flynn-expose" {
		t.Fatalf("expose args %q", got)
	}
}

func TestExtraStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fw.json")
	e := Extra{}.WithPeer("10.0.0.5:1113").WithPort(3001).WithPort(80)
	if err := SaveExtra(path, e); err != nil {
		t.Fatal(err)
	}
	got := LoadExtra(path)
	if !reflect.DeepEqual(got.Peers, []string{"10.0.0.5"}) {
		t.Fatalf("peers %+v", got.Peers)
	}
	if !reflect.DeepEqual(got.Ports, []int{3001}) {
		t.Fatalf("ports %+v", got.Ports)
	}
	got = got.WithoutPeer("10.0.0.5").WithoutPort(3001)
	if len(got.Peers) != 0 || len(got.Ports) != 0 {
		t.Fatalf("cleared %+v", got)
	}
	_ = os.Remove(path)
}

func TestMergeDesiredUnionsLiveAndExtra(t *testing.T) {
	d := MergeDesired("10.0.0.1", Extra{Peers: []string{"10.0.0.2"}, Ports: []int{3001}}, []string{"10.0.0.3"}, []int{3002})
	plan := Plan(d)
	from := map[string]bool{}
	ports := map[int]bool{}
	for _, r := range plan {
		if r.Kind == KindPeer {
			from[r.From] = true
		}
		if r.Kind == KindExpose {
			ports[r.Port] = true
		}
	}
	if !from["10.0.0.2"] || !from["10.0.0.3"] || from["10.0.0.1"] {
		t.Fatalf("peers %+v", from)
	}
	if !ports[3001] || !ports[3002] {
		t.Fatalf("ports %+v", ports)
	}
}
