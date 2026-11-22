package server

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// scanReference is the pre-CORE-41 lookup: list each netpolicy service in
// order and take the first instance whose address is ip.
func scanReference(s *Store, ip net.IP) (string, *discoverd.Instance) {
	for _, service := range netPolicyServices {
		insts, _ := s.Instances(service)
		for _, inst := range insts {
			host, _, err := net.SplitHostPort(inst.Addr)
			if err != nil {
				host = inst.Addr
			}
			if parsed := net.ParseIP(host); parsed != nil && parsed.Equal(ip) {
				return service, inst
			}
		}
	}
	return "", nil
}

func assertNetInstanceMatchesScan(t *testing.T, s *Store, ips []net.IP, step string) {
	t.Helper()
	for _, ip := range ips {
		wantSvc, want := scanReference(s, ip)
		gotSvc, got := s.NetInstance(ip)
		if gotSvc != wantSvc || (got == nil) != (want == nil) || got != nil && (got.ID != want.ID || !got.Equal(want)) {
			t.Fatalf("%s: NetInstance(%s) = %q %+v, scan = %q %+v", step, ip, gotSvc, got, wantSvc, want)
		}
	}
}

// The address index answers exactly as a scan would through adds, address
// changes, removes, expiry, a removed service and a snapshot restore.
func TestStoreNetInstanceMatchesScan(t *testing.T) {
	s := newFSMStore(t)
	services := append([]string{"web"}, netPolicyServices...)
	for _, svc := range services {
		s.addService(t, svc)
	}
	var ips []net.IP
	for i := 0; i < 6; i++ {
		ips = append(ips, net.ParseIP(fmt.Sprintf("100.100.0.%d", i+1)))
	}
	ips = append(ips, net.ParseIP("::ffff:100.100.0.1"), net.ParseIP("fd00::1"))
	addrs := []string{"100.100.0.1:0", "100.100.0.2:80", "100.100.0.3:0", "100.100.0.4:0", "100.100.0.5:0", "[fd00::1]:0", "not-an-addr", "100.100.0.6"}

	rnd := rand.New(rand.NewSource(41))
	for step := 0; step < 2000; step++ {
		svc := services[rnd.Intn(len(services))]
		id := fmt.Sprintf("i%d", rnd.Intn(8))
		var name string
		// Removing from a service with no instances is a NotFoundError;
		// the apply still runs, so errors are ignored here.
		switch op := rnd.Intn(10); {
		case op < 6:
			s.tryApply(t, addInstanceCommandType, &addInstanceCommand{Service: svc, Instance: &discoverd.Instance{
				ID: id, Addr: addrs[rnd.Intn(len(addrs))], Proto: "tcp", Meta: map[string]string{"step": fmt.Sprint(step)}}})
			name = "add"
		case op < 8:
			s.tryApply(t, removeInstanceCommandType, &removeInstanceCommand{Service: svc, ID: id})
			name = "remove"
		default:
			s.tryApply(t, expireInstancesCommandType, &expireInstancesCommand{Instances: []expireInstance{{Service: svc, InstanceID: id}}})
			name = "expire"
		}
		assertNetInstanceMatchesScan(t, s.Store, ips, fmt.Sprintf("step %d %s %s/%s", step, name, svc, id))
	}

	// RemoveService keeps instance records (see applyRemoveServiceCommand),
	// so DNS keeps matching them, as before.
	s.apply(t, removeServiceCommandType, &removeServiceCommand{Service: netpolicy.ServiceSys})
	assertNetInstanceMatchesScan(t, s.Store, ips, "remove service")

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := snap.Persist(&bufSink{Buffer: &buf}); err != nil {
		t.Fatal(err)
	}
	restored := newFSMStore(t)
	if err := restored.Restore(io.NopCloser(&buf)); err != nil {
		t.Fatal(err)
	}
	assertNetInstanceMatchesScan(t, restored.Store, ips, "restore")
	for _, ip := range ips {
		a, ai := s.NetInstance(ip)
		b, bi := restored.NetInstance(ip)
		if a != b || (ai == nil) != (bi == nil) || ai != nil && ai.ID != bi.ID {
			t.Fatalf("restore: NetInstance(%s) = %q %+v, before %q %+v", ip, b, bi, a, ai)
		}
	}
}

// NetInstance returns a copy, and its cost does not grow with the number of
// registered jobs.
func TestStoreNetInstanceCopiesOneInstance(t *testing.T) {
	small, large := manyUserJobsStore(t, 10), manyUserJobsStore(t, 2000)
	for _, s := range []*fsmStore{small, large} {
		s.addInstance(t, netpolicy.ServiceUser, netUserInst("me", "100.101.0.1", "x"))
	}
	ip := net.ParseIP("100.101.0.1")
	_, inst := large.NetInstance(ip)
	if inst == nil || inst.ID != "me" {
		t.Fatalf("NetInstance = %+v", inst)
	}
	inst.Addr, inst.Index = "100.101.9.9:1", 999999
	if _, again := large.NetInstance(ip); again == nil || again.Addr != "100.101.0.1:0" || again.Index == 999999 {
		t.Fatal("NetInstance returned the stored instance, not a copy")
	}
	allocs := func(s *fsmStore) float64 {
		return testing.AllocsPerRun(50, func() { s.NetInstance(ip) })
	}
	if a, b := allocs(small), allocs(large); b > a || b > 10 {
		t.Fatalf("NetInstance allocations: %v with 10 jobs, %v with 2000", a, b)
	}
}

// DNS read paths take the read lock, so lookups run side by side and do not
// queue behind each other for the lock raft apply needs.
func TestStoreReadPathsTakeReadLock(t *testing.T) {
	s := manyUserJobsStore(t, 10)
	s.mu.RLock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Instances("postgres")
		s.ServiceLeader("postgres")
		s.ServiceMeta("postgres")
		s.NetInstance(net.ParseIP("100.100.1.1"))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a read path waited for the exclusive lock while a reader held it")
	}
	s.mu.RUnlock()
}

type bufSink struct{ *bytes.Buffer }

func (bufSink) ID() string    { return "test" }
func (bufSink) Cancel() error { return nil }
func (bufSink) Close() error  { return nil }

// proxyPeer is a Raft-peer discoverd HTTP API over s that counts instance
// list requests for the netpolicy services.
func proxyPeer(t *testing.T, s *Store) (string, *int32) {
	t.Helper()
	h := NewHandler(false, nil)
	h.Store = s
	var lists int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/services/flynn-net-") && strings.HasSuffix(r.URL.Path, "/instances") {
			atomic.AddInt32(&lists, 1)
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &lists
}

func waitNetInstance(t *testing.T, p *ProxyStore, ip net.IP, wantSvc, wantID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		svc, inst := p.NetInstance(ip)
		id := ""
		if inst != nil {
			id = inst.ID
		}
		if svc == wantSvc && id == wantID {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("NetInstance(%s) = %q %q, want %q %q", ip, svc, id, wantSvc, wantID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// CORE-25-F1-F2: a proxy-mode discoverd answers netpolicy lookups from event
// streams, and lists services over HTTP only until those are current.
func TestProxyStoreNetInstanceFollowsStreams(t *testing.T) {
	s := openLeaderStore(t)
	for _, svc := range netPolicyServices {
		if err := s.AddService(svc, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddInstance(netpolicy.ServiceUser, netUserInst("u1", "100.100.0.1", "redis-a")); err != nil {
		t.Fatal(err)
	}
	peer, lists := proxyPeer(t, s)
	p := &ProxyStore{Peers: []string{peer}}
	defer p.Close()

	userIP, sysIP := net.ParseIP("100.100.0.1"), net.ParseIP("100.100.0.2")
	// The first lookups may list services while the streams connect.
	waitNetInstance(t, p, userIP, netpolicy.ServiceUser, "u1")
	waitNetInstance(t, p, sysIP, "", "")
	deadline := time.Now().Add(10 * time.Second)
	for {
		p.netIdx.mu.RLock()
		n := 0
		for _, svc := range netPolicyServices {
			if p.netIdx.current[svc] {
				n++
			}
		}
		p.netIdx.mu.RUnlock()
		if n == len(netPolicyServices) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("netpolicy streams never became current")
		}
		time.Sleep(10 * time.Millisecond)
	}

	before := atomic.LoadInt32(lists)
	if err := s.AddInstance(netpolicy.ServiceSys, &discoverd.Instance{ID: "s1", Addr: "100.100.0.2:0", Proto: "tcp"}); err != nil {
		t.Fatal(err)
	}
	waitNetInstance(t, p, sysIP, netpolicy.ServiceSys, "s1")
	if _, inst := p.NetInstance(userIP); inst == nil || inst.Meta[netpolicy.MetaDiscoverdAllow] != "redis-a" {
		t.Fatalf("user instance meta lost: %+v", inst)
	}
	if err := s.RemoveInstance(netpolicy.ServiceUser, "u1"); err != nil {
		t.Fatal(err)
	}
	waitNetInstance(t, p, userIP, "", "")
	if after := atomic.LoadInt32(lists); after != before {
		t.Fatalf("current streams still listed services over HTTP: %d requests", after-before)
	}

	// Closed: back to listing, still correct.
	p.Close()
	if svc, inst := p.NetInstance(sysIP); svc != netpolicy.ServiceSys || inst == nil || inst.ID != "s1" {
		t.Fatalf("after Close: NetInstance = %q %+v", svc, inst)
	}
	if atomic.LoadInt32(lists) == before {
		t.Fatal("after Close: expected an HTTP list")
	}
}
