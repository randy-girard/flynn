package server

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// netPolicyServices are the services flynn-host registers each overlay job
// in, in the order a DNS client is matched against them.
var netPolicyServices = []string{netpolicy.ServiceUser, netpolicy.ServiceSys, netpolicy.ServiceData, netpolicy.ServiceBuild}

func isNetPolicyService(service string) bool {
	for _, s := range netPolicyServices {
		if s == service {
			return true
		}
	}
	return false
}

// NetPolicyIndex is implemented by DNS stores that keep the netpolicy
// services indexed by address (CORE-41).
type NetPolicyIndex interface {
	// NetInstance returns the first netpolicy service (user, then sys,
	// data, build) that lists ip, and a copy of its instance. It returns ""
	// and nil when no netpolicy service lists ip.
	NetInstance(ip net.IP) (service string, inst *discoverd.Instance)
}

// netIndex maps the instance addresses of the netpolicy services to their
// instances. It is not safe for concurrent use.
type netIndex struct {
	// byIP is service -> IP -> instance ID -> instance.
	byIP map[string]map[string]map[string]*discoverd.Instance
	// ipOf is service -> instance ID -> IP key in byIP.
	ipOf map[string]map[string]string
}

func newNetIndex() *netIndex {
	return &netIndex{
		byIP: make(map[string]map[string]map[string]*discoverd.Instance),
		ipOf: make(map[string]map[string]string),
	}
}

// instanceIP is the index key for an instance address, or "" when the
// address has no parseable IP (such an instance never matches a client).
func instanceIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// put adds or replaces inst. Services outside netPolicyServices are ignored.
func (x *netIndex) put(service string, inst *discoverd.Instance) {
	if inst == nil || !isNetPolicyService(service) {
		return
	}
	x.remove(service, inst.ID)
	ip := instanceIP(inst.Addr)
	if ip == "" {
		return
	}
	ips := x.byIP[service]
	if ips == nil {
		ips = make(map[string]map[string]*discoverd.Instance)
		x.byIP[service] = ips
	}
	ids := ips[ip]
	if ids == nil {
		ids = make(map[string]*discoverd.Instance, 1)
		ips[ip] = ids
	}
	ids[inst.ID] = inst
	if x.ipOf[service] == nil {
		x.ipOf[service] = make(map[string]string)
	}
	x.ipOf[service][inst.ID] = ip
}

// remove drops the instance id of service, if it is indexed.
func (x *netIndex) remove(service, id string) {
	ip, ok := x.ipOf[service][id]
	if !ok {
		return
	}
	delete(x.ipOf[service], id)
	ids := x.byIP[service][ip]
	delete(ids, id)
	if len(ids) == 0 {
		delete(x.byIP[service], ip)
	}
}

// clear drops every instance of service.
func (x *netIndex) clear(service string) {
	delete(x.byIP, service)
	delete(x.ipOf, service)
}

// lookup returns the instance of service whose address is ip. When several
// share the address it returns the oldest (lowest Index), which is the one
// a scan of the sorted instance list finds first.
func (x *netIndex) lookup(service string, ip net.IP) *discoverd.Instance {
	var found *discoverd.Instance
	for _, inst := range x.byIP[service][ip.String()] {
		if found == nil || inst.Index < found.Index || inst.Index == found.Index && inst.ID < found.ID {
			found = inst
		}
	}
	return found
}

// rebuildNetIndex indexes the netpolicy services of d.
func rebuildNetIndex(d *raftData) *netIndex {
	x := newNetIndex()
	for _, service := range netPolicyServices {
		for _, inst := range d.Instances[service] {
			x.put(service, inst)
		}
	}
	return x
}

// NetInstance implements NetPolicyIndex. It takes the read lock and copies
// one instance, however many jobs are registered.
func (s *Store) NetInstance(ip net.IP) (string, *discoverd.Instance) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, service := range netPolicyServices {
		if inst := s.netIdx.lookup(service, ip); inst != nil {
			return service, inst.Copy()
		}
	}
	return "", nil
}

// scanNetInstance is the instance of service whose address is ip, found by
// listing the service. It is the fallback for stores without an index and
// for a proxy whose watch of service is not current.
func scanNetInstance(store DNSStore, service string, ip net.IP) *discoverd.Instance {
	insts, err := store.Instances(service)
	if err != nil || len(insts) == 0 {
		return nil
	}
	key := ip.String()
	for _, inst := range insts {
		if inst != nil && instanceIP(inst.Addr) == key {
			return inst
		}
	}
	return nil
}

// proxyNetIndex keeps the netpolicy services of a proxy-mode discoverd
// indexed from event streams to the Raft peers, so a DNS query does not cost
// up to four HTTP round trips (CORE-25-F1-F2). A service whose stream is not
// current (not connected yet, or reconnecting) is not answered from the
// index.
type proxyNetIndex struct {
	mu      sync.RWMutex
	idx     *netIndex
	current map[string]bool

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// proxyWatchRetry is the first delay before a stream is reopened. It
// doubles up to proxyWatchMaxRetry while the stream keeps failing before it
// is current.
var (
	proxyWatchRetry    = time.Second
	proxyWatchMaxRetry = 30 * time.Second
)

func startProxyNetIndex(peers []string) *proxyNetIndex {
	p := &proxyNetIndex{
		idx:     newNetIndex(),
		current: make(map[string]bool),
		stop:    make(chan struct{}),
	}
	urls := make([]string, len(peers))
	for i, peer := range peers {
		urls[i] = "http://" + peer
	}
	// Spread proxies over the peers; the client fails over to the others.
	rand.Shuffle(len(urls), func(i, j int) { urls[i], urls[j] = urls[j], urls[i] })
	for _, service := range netPolicyServices {
		p.wg.Add(1)
		go p.watch(discoverd.NewClientWithURL(strings.Join(urls, ",")), service)
	}
	return p
}

func (p *proxyNetIndex) watch(client *discoverd.Client, service string) {
	defer p.wg.Done()
	delay := proxyWatchRetry
	for {
		wasCurrent, err := p.stream(client, service)
		if wasCurrent {
			delay = proxyWatchRetry
		}
		if err != nil {
			logger.Warn("netpolicy index stream ended, DNS lists the service until it is current again", "fn", "proxyNetIndex.watch", "service", service, "retry", delay, "err", err)
		}
		select {
		case <-p.stop:
			return
		case <-time.After(delay):
		}
		if !wasCurrent && delay < proxyWatchMaxRetry {
			delay *= 2
			if delay > proxyWatchMaxRetry {
				delay = proxyWatchMaxRetry
			}
		}
	}
}

// stream follows one event stream of service until it ends or p stops. It
// reports whether the stream became current.
func (p *proxyNetIndex) stream(client *discoverd.Client, service string) (isCurrent bool, err error) {
	events := make(chan *discoverd.Event)
	st, err := client.Stream("GET", fmt.Sprintf("/services/%s", service), nil, events)
	if err != nil {
		return false, err
	}
	defer st.Close()
	defer func() {
		p.mu.Lock()
		p.current[service] = false
		p.idx.clear(service)
		p.mu.Unlock()
	}()
	// Instances sent before EventKindCurrent are the snapshot; build it
	// apart so a reconnect never answers from a half-filled index.
	snapshot := newNetIndex()
	for {
		select {
		case <-p.stop:
			return isCurrent, nil
		case ev, ok := <-events:
			if !ok {
				if err := st.Err(); err != nil {
					return isCurrent, err
				}
				return isCurrent, errors.New("stream closed")
			}
			p.apply(snapshot, &isCurrent, service, ev)
		}
	}
}

func (p *proxyNetIndex) apply(snapshot *netIndex, isCurrent *bool, service string, ev *discoverd.Event) {
	if !*isCurrent {
		switch ev.Kind {
		case discoverd.EventKindUp, discoverd.EventKindUpdate:
			snapshot.put(service, ev.Instance)
		case discoverd.EventKindDown:
			if ev.Instance != nil {
				snapshot.remove(service, ev.Instance.ID)
			}
		case discoverd.EventKindCurrent:
			p.mu.Lock()
			p.idx.clear(service)
			for _, ids := range snapshot.byIP[service] {
				for _, inst := range ids {
					p.idx.put(service, inst)
				}
			}
			p.current[service] = true
			p.mu.Unlock()
			*isCurrent = true
		}
		return
	}
	switch ev.Kind {
	case discoverd.EventKindUp, discoverd.EventKindUpdate:
		p.mu.Lock()
		p.idx.put(service, ev.Instance)
		p.mu.Unlock()
	case discoverd.EventKindDown:
		if ev.Instance != nil {
			p.mu.Lock()
			p.idx.remove(service, ev.Instance.ID)
			p.mu.Unlock()
		}
	}
}

// lookup returns the instance of service at ip, and whether the index is
// current for service.
func (p *proxyNetIndex) lookup(service string, ip net.IP) (*discoverd.Instance, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.current[service] {
		return nil, false
	}
	if inst := p.idx.lookup(service, ip); inst != nil {
		return inst.Copy(), true
	}
	return nil, true
}

func (p *proxyNetIndex) close() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.wg.Wait()
}
