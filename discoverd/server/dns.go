package server

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	reuseport "github.com/kavu/go_reuseport"
	"github.com/miekg/dns"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/keepalive"
	"github.com/randy-girard/flynn/pkg/netpolicy"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/random"
)

type DNSStore interface {
	Instances(service string) ([]*discoverd.Instance, error)
	ServiceLeader(service string) (*discoverd.Instance, error)
}

// LocalJobs looks up the netpolicy registration that the local flynn-host
// holds for one of its own overlay IPs. It returns nil and no error when no
// running job on the host owns the IP.
type LocalJobs interface {
	OverlayInstance(ip net.IP) (*discoverd.Instance, error)
}

type DNSServer struct {
	UDPAddr   string
	TCPAddr   string
	Domain    string
	Recursors []string

	// Overlay is the cluster overlay network. A *.discoverd query from an
	// IP in it that no netpolicy service lists is answered as a user job
	// with no attached datastores, unless LocalJobs says otherwise. Nil
	// turns that off: every unlisted client gets system answers.
	Overlay *net.IPNet
	// LocalSubnet is this host's slice of Overlay. Unlisted clients in it
	// are looked up in LocalJobs, because a job's own registration can
	// still be on its way through Raft when it sends its first query.
	LocalSubnet *net.IPNet
	LocalJobs   LocalJobs
	// SelfIPs are this host's own addresses in Overlay (the bridge
	// address). Host-network jobs query from them; they are system clients.
	SelfIPs []net.IP

	store   atomic.Value // *DNSStore
	servers []*dns.Server
}

func (srv *DNSServer) GetStore() DNSStore {
	return *srv.store.Load().(*DNSStore)
}

func (srv *DNSServer) SetStore(s DNSStore) {
	srv.store.Store(&s)
}

const maxUDPRecords = 3
const dnsDomain = "discoverd."

func (srv *DNSServer) ListenAndServe() error {
	if srv.GetStore() == nil {
		panic("missing Store")
	}
	if srv.Domain == "" {
		srv.Domain = dnsDomain
	}
	if err := srv.validateRecursors(); err != nil {
		return err
	}

	api := dnsAPI{srv}
	mux := dns.NewServeMux()
	mux.HandleFunc(srv.Domain, api.ServiceLookup)
	if len(srv.Recursors) > 0 {
		mux.HandleFunc(".", api.Recurse)
	}

	errors := make(chan error, 4)
	done := func() { errors <- nil }

	if srv.UDPAddr != "" {
		l, err := reuseport.NewReusablePortPacketConn("udp4", srv.UDPAddr)
		if err != nil {
			return err
		}
		srv.UDPAddr = l.(*net.UDPConn).LocalAddr().String()
		server := &dns.Server{
			Net:               "udp",
			PacketConn:        l,
			Handler:           mux,
			NotifyStartedFunc: done,
		}
		go func() { errors <- server.ActivateAndServe() }()
		srv.servers = append(srv.servers, server)
	}

	if srv.TCPAddr != "" {
		l, err := keepalive.ReusableListen("tcp4", srv.TCPAddr)
		if err != nil {
			return err
		}
		srv.TCPAddr = l.Addr().String()
		server := &dns.Server{
			Net:               "tcp",
			Listener:          l,
			Handler:           mux,
			NotifyStartedFunc: done,
		}
		go func() { errors <- server.ActivateAndServe() }()
		srv.servers = append(srv.servers, server)
	}

	for range srv.servers {
		if err := <-errors; err != nil {
			return err
		}
	}
	return nil
}

func (srv *DNSServer) validateRecursors() error {
	for i, r := range srv.Recursors {
		_, _, err := net.SplitHostPort(r)
		if e, ok := err.(*net.AddrError); ok {
			switch e.Err {
			case "missing port in address":
				r = r + ":53"
			case "too many colons in address":
				// Assume a bare IPv6 address
				r = fmt.Sprintf("[%s]:53", r)
			}
		} else if err != nil {
			return fmt.Errorf("discoverd: invalid recursor address %s: %s", r, err)
		}
		addr, err := net.ResolveTCPAddr("tcp", r)
		if err != nil {
			return fmt.Errorf("discoverd: unable to resolve recursor address %s: %s", r, err)
		}
		srv.Recursors[i] = addr.String()
	}
	return nil
}

func (srv *DNSServer) Close() error {
	var err error
	for _, s := range srv.servers {
		e := s.Shutdown()
		if err == nil {
			err = e
		}
	}
	return err
}

type dnsAPI struct {
	*DNSServer
}

func (d dnsAPI) Recurse(w dns.ResponseWriter, req *dns.Msg) {
	var client dns.Client

	if isTCP(w.RemoteAddr()) {
		client.Net = "tcp"
	}

	for _, recursor := range d.Recursors {
		req.Compress = true
		res, _, err := client.Exchange(req, recursor)
		if err != nil {
			continue
		}
		res.Compress = true
		w.WriteMsg(res)
		return
	}

	// Return SERVFAIL
	res := &dns.Msg{}
	res.RecursionAvailable = true
	res.SetRcode(req, dns.RcodeServerFailure)
	w.WriteMsg(res)
}

func (d dnsAPI) ServiceLookup(w dns.ResponseWriter, req *dns.Msg) {
	qName := req.Question[0].Name
	qType := req.Question[0].Qtype
	name := strings.TrimSuffix(strings.ToLower(dns.Fqdn(qName)), d.Domain)
	labels := dns.SplitDomainName(name)
	tcp := isTCP(w.RemoteAddr())

	res := &dns.Msg{}
	res.Authoritative = true
	res.Compress = true
	res.RecursionAvailable = len(d.Recursors) > 0
	res.SetReply(req)
	defer func() {
		if res.Rcode == dns.RcodeSuccess && qType == dns.TypeSOA {
			// SOA answer if requested. at the end of the request to ensure we didn't hit NXDOMAIN
			res.Answer = []dns.RR{d.soaRecord()}
		}
		if len(res.Answer) == 0 {
			// Add authority section with SOA if the answer has no items
			res.Ns = []dns.RR{d.soaRecord()}
		}
		w.WriteMsg(res)
	}()

	nxdomain := func() { res.SetRcode(req, dns.RcodeNameError) }

	var service string
	var proto string
	var instanceID string
	var leader bool
	switch {
	case len(labels) == 1:
		// normal lookup
		service = labels[0]
	case len(labels) == 2 && strings.HasPrefix(labels[0], "_") && strings.HasPrefix(labels[1], "_"):
		// RFC 2782 request looks like _postgres._tcp
		service = labels[0][1:]
		proto = labels[1][1:]
	case len(labels) == 3 && labels[2] == "_i":
		// address lookup for instance in RFC 2782 SRV record
		service = labels[1]
		instanceID = labels[0]
	case len(labels) == 2 && labels[0] == "leader":
		// leader lookup
		leader = true
		service = labels[1]
	default:
		nxdomain()
		return
	}

	if inst := d.userNetInstance(w.RemoteAddr()); inst != nil {
		allowed, restrict := netpolicy.ParseDiscoverdAllow(inst.Meta)
		if restrict {
			if !netpolicy.UserMayResolveAttached(leader, service, allowed) {
				nxdomain()
				return
			}
		} else if !d.userMayResolveDiscoverd(leader, service) {
			nxdomain()
			return
		}
	}

	var instances []*discoverd.Instance
	if !leader {
		a, err := d.GetStore().Instances(service)
		if err != nil {
			log.Printf("discoverd: dns: cannot retrieve instances: %s", err)
			nxdomain()
			return
		} else if a == nil {
			nxdomain()
			return
		}
		instances = a
	}

	if leader || instanceID != "" {
		// we're doing a lookup for a single instance
		var resInst *discoverd.Instance
		if leader {
			sl, err := d.GetStore().ServiceLeader(service)
			if err != nil {
				log.Printf("discoverd: dns: cannot retrieve service leader: %s", err)
				nxdomain()
				return
			}
			resInst = sl
		} else {
			for _, inst := range instances {
				if inst.ID == instanceID {
					resInst = inst
					break
				}
			}
		}
		if resInst == nil {
			nxdomain()
			return
		}

		addr := parseAddr(resInst)
		if qType != dns.TypeA && qType != dns.TypeAAAA && qType != dns.TypeANY && qType != dns.TypeSRV ||
			addr.IPv4 == nil && qType == dns.TypeA ||
			addr.IPv6 == nil && qType == dns.TypeAAAA {
			// no results if we're looking up an record that doesn't match the
			// request type or the type is incorrect
			return
		}
		res.Answer = make([]dns.RR, 0, 2)
		if qType != dns.TypeSRV {
			res.Answer = append(res.Answer, addrRecord(qName, addr))
		}
		if qType == dns.TypeSRV || qType == dns.TypeANY {
			res.Answer = append(res.Answer, d.srvRecord(qName, service, addr, false))
		}
		if tcp && qType == dns.TypeSRV {
			res.Extra = []dns.RR{addrRecord(qName, addr)}
		}
		return
	}

	if qType == dns.TypeSOA {
		// We don't need to do any more processing, as NXDOMAIN can't be reached
		// beyond this point, the SOA answer is added in the deferred function
		// above
		return
	}

	addrs := make([]*addrData, 0, len(instances))
	added := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		if proto != "" && inst.Proto != proto {
			continue
		}
		addr := parseAddr(inst)
		if _, ok := added[addr.String]; ok {
			continue
		}
		if addr.IPv4 == nil && qType == dns.TypeA || addr.IPv6 == nil && qType == dns.TypeAAAA {
			// Skip instance if we have an IPv6 address but want IPv4 or vice versa
			continue
		}
		if qType != dns.TypeSRV {
			// skip duplicate IPs if we're not doing an SRV lookup
			added[addr.String] = struct{}{}
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		// return empty response
		return
	}
	shuffle(addrs)

	// Truncate the response if we're using UDP
	if !tcp && len(addrs) > maxUDPRecords {
		addrs = addrs[:maxUDPRecords]
	}

	res.Answer = make([]dns.RR, 0, len(addrs)*2)
	for _, addr := range addrs {
		if qType == dns.TypeANY || qType == dns.TypeA || qType == dns.TypeAAAA {
			res.Answer = append(res.Answer, addrRecord(qName, addr))
		}
	}
	for _, addr := range addrs {
		if qType == dns.TypeANY || qType == dns.TypeSRV {
			res.Answer = append(res.Answer, d.srvRecord(qName, service, addr, true))
		}
	}

	if qType == dns.TypeSRV && tcp {
		// Add extra records mapping instance IDs to addresses
		res.Extra = make([]dns.RR, len(addrs))
		for i, addr := range addrs {
			res.Extra[i] = addrRecord(d.instanceDomain(service, addr.ID), addr)
		}
	}
}

func (d dnsAPI) soaRecord() dns.RR {
	return &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   d.Domain,
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
		},
		Ns:      "ns." + d.Domain,
		Mbox:    "postmaster." + d.Domain,
		Serial:  uint32(time.Now().Unix()),
		Refresh: 3600,
		Retry:   600,
		Expire:  86400,
	}
}

func (d dnsAPI) srvRecord(name, service string, addr *addrData, instTarget bool) dns.RR {
	r := &dns.SRV{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeSRV,
			Class:  dns.ClassINET,
		},
		Priority: 1,
		Weight:   1,
		Port:     addr.Port,
		Target:   name,
	}
	if instTarget {
		r.Target = d.instanceDomain(service, addr.ID)
	}
	return r
}

func (d dnsAPI) instanceDomain(service, id string) string {
	return fmt.Sprintf("%s.%s._i.%s", id, service, d.Domain)
}

func addrRecord(name string, addr *addrData) dns.RR {
	if addr.IPv6 != nil {
		return &dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET,
			},
			AAAA: addr.IPv6,
		}
	}
	return &dns.A{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
		},
		A: addr.IPv4,
	}
}

type addrData struct {
	IPv6   net.IP
	IPv4   net.IP
	String string
	Port   uint16
	ID     string
}

func parseAddr(inst *discoverd.Instance) *addrData {
	res := &addrData{ID: inst.ID}
	ip, port, _ := net.SplitHostPort(inst.Addr)
	res.String = ip
	portInt, _ := strconv.Atoi(port)
	res.Port = uint16(portInt)
	ipBytes := net.ParseIP(ip)
	res.IPv4 = ipBytes.To4()
	if res.IPv4 == nil {
		res.IPv6 = ipBytes
	}
	return res
}

func shuffle(s []*addrData) []*addrData {
	for i := len(s) - 1; i > 0; i-- {
		j := random.Math.Intn(i + 1)
		s[i], s[j] = s[j], s[i]
	}
	return s
}

func isTCP(addr net.Addr) bool {
	_, ok := addr.(*net.TCPAddr)
	return ok
}

func remoteIP(addr net.Addr) net.IP {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.IP
	case *net.TCPAddr:
		return a.IP
	default:
		if addr == nil {
			return nil
		}
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return net.ParseIP(addr.String())
		}
		return net.ParseIP(host)
	}
}

func (d dnsAPI) userMayResolveDiscoverd(leader bool, service string) bool {
	if netpolicy.UserMayResolveDiscoverd(leader, service) {
		return true
	}
	if !leader {
		return false
	}
	// Discoverd jobs cannot read host /etc/flynn/installed-plugins.json.
	// Shared plugin appliances (mariadb, mongodb) advertise flynn-datastore
	// on their instances so user jobs can resolve leader.<service>.discoverd.
	store := d.GetStore()
	if store == nil {
		return false
	}
	insts, err := store.Instances(service)
	if err == nil {
		for _, inst := range insts {
			if inst != nil && inst.Meta[plugin.MetaDatastore] == "true" {
				return true
			}
		}
	}
	if sl, err := store.ServiceLeader(service); err == nil && sl != nil && sl.Meta[plugin.MetaDatastore] == "true" {
		return true
	}
	return false
}

// failClosedInstance stands in for a user job with no attached datastores:
// every *.discoverd name is NXDOMAIN for it.
var failClosedInstance = &discoverd.Instance{Meta: map[string]string{netpolicy.MetaDiscoverdAllow: ""}}

// userNetInstance returns the user registration that restricts a DNS
// client, or nil for a system client that gets every answer.
//
// A client listed in flynn-net-user is a user job. Clients outside the
// overlay network (host-network jobs, the host itself) are system clients.
// An overlay client listed in another netpolicy service is a system,
// datastore or build job. Any other overlay client is unknown and fails
// closed, except that the local flynn-host is asked about its own IPs: a
// job's registration reaches the leader before the job starts, but this
// discoverd can apply it a few milliseconds later than the job's first
// query, and a failed registration is only retried every 15 s.
func (d dnsAPI) userNetInstance(addr net.Addr) *discoverd.Instance {
	ip := remoteIP(addr)
	if ip == nil {
		return nil
	}
	service, inst := d.netInstance(ip)
	if service == netpolicy.ServiceUser {
		return inst
	}
	if d.Overlay == nil || !d.Overlay.Contains(ip) {
		return nil
	}
	for _, self := range d.SelfIPs {
		if self.Equal(ip) {
			return nil
		}
	}
	if service != "" {
		// flynn-net-sys, flynn-net-data or flynn-net-build.
		return nil
	}
	if d.LocalJobs != nil && d.LocalSubnet != nil && d.LocalSubnet.Contains(ip) {
		inst, err := d.LocalJobs.OverlayInstance(ip)
		if err != nil {
			log.Printf("discoverd: dns: cannot look up overlay ip %s on the local host: %s", ip, err)
		} else if inst != nil {
			switch inst.Meta["class"] {
			case netpolicy.ClassUser.String():
				if _, ok := netpolicy.ParseDiscoverdAllow(inst.Meta); !ok {
					// Never the legacy env-wide answer for a host lookup.
					return failClosedInstance
				}
				return inst
			case netpolicy.ClassSystem.String(), netpolicy.ClassDatastore.String(), netpolicy.ClassBuild.String():
				return nil
			}
		}
	}
	return failClosedInstance
}

// netInstance returns the first netpolicy service (user, sys, data, build)
// that lists ip, and its instance. Stores with an address index answer in
// one lookup (CORE-41); others are scanned service by service.
func (d dnsAPI) netInstance(ip net.IP) (string, *discoverd.Instance) {
	store := d.GetStore()
	if idx, ok := store.(NetPolicyIndex); ok {
		return idx.NetInstance(ip)
	}
	for _, service := range netPolicyServices {
		if inst := scanNetInstance(store, service, ip); inst != nil {
			return service, inst
		}
	}
	return "", nil
}
