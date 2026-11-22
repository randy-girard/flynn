package server

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/hashicorp/raft"
	"github.com/miekg/dns"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/netpolicy"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// Hook gocheck up to the "go test" runner
func Test(t *testing.T) { TestingT(t) }

type DNSSuite struct{}

var _ = Suite(&DNSSuite{})

func (s *DNSSuite) newServer(c *C, recursors []string) *DNSServer {
	srv := &DNSServer{
		UDPAddr:   "127.0.0.1:0",
		TCPAddr:   "127.0.0.1:0",
		Recursors: recursors,
	}
	srv.SetStore(&DNSServerStore{
		InstancesFn:     func(service string) ([]*discoverd.Instance, error) { return nil, nil },
		ServiceLeaderFn: func(service string) (*discoverd.Instance, error) { return nil, nil },
	})
	c.Assert(srv.ListenAndServe(), IsNil)
	return srv
}

func (s *DNSSuite) TestRecursorParsing(c *C) {
	srv := s.newServer(c, []string{
		"2001:4860:4860::8844",
		"[2001:4860:4860::8888]:553",
		"8.8.8.8",
		"8.8.4.4:5553",
		"google-public-dns-a.google.com",
		"google-public-dns-b.google.com:55553",
	})
	srv.Close()
	c.Assert(srv.Recursors, DeepEquals, []string{
		"[2001:4860:4860::8844]:53",
		"[2001:4860:4860::8888]:553",
		"8.8.8.8:53",
		"8.8.4.4:5553",
		"8.8.8.8:53",
		"8.8.4.4:55553",
	})
}

func startUpstreamTestServer(c *C) (string, string, func()) {
	h := dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		res := &dns.Msg{}
		res.SetReply(req)
		switch req.Question[0].Name {
		case "long-compressed-response.":
			for i := 0; i < 25; i++ {
				res.Answer = append(res.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   req.Question[0].Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
					},
					A: net.IP{192, 168, 0, byte(i)},
				})
			}
			if res.Len() <= 512 {
				panic("not huge")
			}
			res.Compress = true
			if res.Len() > 512 {
				panic("too big compressed")
			}
			w.WriteMsg(res)
		}
	})
	up := make(chan struct{}, 2)
	notifyStart := func() { up <- struct{}{} }

	udpListener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	c.Assert(err, IsNil)
	udp := &dns.Server{
		Net:               "udp",
		PacketConn:        udpListener,
		Handler:           h,
		NotifyStartedFunc: notifyStart,
	}
	go udp.ActivateAndServe()

	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	c.Assert(err, IsNil)
	tcp := &dns.Server{
		Net:               "tcp",
		Listener:          tcpListener,
		Handler:           h,
		NotifyStartedFunc: notifyStart,
	}
	go tcp.ActivateAndServe()

	for i := 0; i < 2; i++ {
		select {
		case <-up:
		case <-time.After(5 * time.Second):
			c.Fatal("timed out waiting for server to start")
		}
	}

	return udpListener.(*net.UDPConn).LocalAddr().String(), tcpListener.Addr().String(), func() {
		udp.Shutdown()
		tcp.Shutdown()
	}
}

func (s *DNSSuite) TestRecursor(c *C) {
	udpAddr, tcpAddr, cleanup := startUpstreamTestServer(c)
	defer cleanup()

	withResolvers := func(resolvers []string, net string, f func(string)) {
		srv := s.newServer(c, resolvers)
		defer func() { c.Assert(srv.Close(), IsNil) }()
		addr := srv.UDPAddr
		if net == "tcp" {
			addr = srv.TCPAddr
		}
		f(addr)
	}

	for net, upstreamAddr := range map[string]string{"tcp": tcpAddr, "udp": udpAddr} {
		c.Log(net)
		client := &dns.Client{
			ReadTimeout: 10 * time.Second,
			Net:         net,
		}
		msg := &dns.Msg{}
		msg.SetQuestion("long-compressed-response.", dns.TypeA)

		// Valid request
		withResolvers([]string{upstreamAddr}, net, func(addr string) {
			res, _, err := client.Exchange(msg, addr)
			c.Assert(err, IsNil)
			c.Assert(res.Rcode, Equals, dns.RcodeSuccess)
			c.Assert(len(res.Answer) > 0, Equals, true)
		})

		// Failing recursor fallback
		withResolvers([]string{"127.1.1.1:55", upstreamAddr}, net, func(addr string) {
			res, _, err := client.Exchange(msg, addr)
			c.Assert(err, IsNil)
			c.Assert(res.Rcode, Equals, dns.RcodeSuccess)
			c.Assert(len(res.Answer) > 0, Equals, true)
		})

		// All failing
		withResolvers([]string{"127.1.1.1:55"}, net, func(addr string) {
			res, _, err := client.Exchange(msg, addr)
			c.Assert(err, IsNil)
			c.Assert(res.Rcode, Equals, dns.RcodeServerFailure)
		})
	}
}

func (s *DNSSuite) TestServiceLookup(c *C) {
	type test struct {
		name   string
		domain string
		qs     map[uint16][]testAddr
		data   []*discoverd.Instance
		net    string
	}

	simpleData := make([]*discoverd.Instance, 3)
	simpleAddrs := make([]testAddr, 3)
	simpleData[0], simpleAddrs[0] = fakeStaticInstance("tcp", "192.168.0.1", 80)
	simpleData[1], simpleAddrs[1] = fakeStaticInstance("tcp", "192.168.0.2", 81)
	simpleData[2], simpleAddrs[2] = fakeStaticInstance("tcp", "192.168.0.3", 82)

	mixedData := make([]*discoverd.Instance, 3)
	mixedAddrs := make([]testAddr, 3)
	copy(mixedData, simpleData)
	copy(mixedAddrs, simpleAddrs)
	mixedData[0], mixedAddrs[0] = fakeStaticInstance("udp", "192.168.0.5", 99)
	filteredAddrs := mixedAddrs[:1]

	v6v4Data := make([]*discoverd.Instance, 3)
	v6v4Addrs := make([]testAddr, 3)
	copy(v6v4Data, simpleData)
	copy(v6v4Addrs, simpleAddrs)
	v6v4Data[0], v6v4Addrs[0] = fakeStaticInstance("tcp", "fe80::bae8:56ff:fe46:243c", 22)

	longData := make([]*discoverd.Instance, 5)
	longAddrs := make([]testAddr, 5)
	copy(longData, simpleData)
	copy(longAddrs, simpleAddrs)
	longData[3], longAddrs[3] = fakeStaticInstance("tcp", "192.168.0.4", 83)
	longData[4], longAddrs[4] = fakeStaticInstance("tcp", "192.168.0.5", 84)

	dupeData := make([]*discoverd.Instance, 3)
	dupeAddrs := make([]testAddr, 3)
	copy(dupeData, simpleData)
	copy(dupeAddrs, simpleAddrs)
	dupeData[1], dupeAddrs[1] = fakeStaticInstance("tcp", "192.168.0.1", 85)

	emptyAll := map[uint16][]testAddr{
		dns.TypeA:    nil,
		dns.TypeAAAA: nil,
		dns.TypeANY:  nil,
		dns.TypeSRV:  nil,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	simpleQs := map[uint16][]testAddr{
		dns.TypeA:    simpleAddrs,
		dns.TypeAAAA: nil,
		dns.TypeANY:  simpleAddrs,
		dns.TypeSRV:  simpleAddrs,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	udpFiltered := map[uint16][]testAddr{
		dns.TypeA:    filteredAddrs,
		dns.TypeAAAA: nil,
		dns.TypeANY:  filteredAddrs,
		dns.TypeSRV:  filteredAddrs,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	mixedQs := map[uint16][]testAddr{
		dns.TypeA:    mixedAddrs,
		dns.TypeAAAA: nil,
		dns.TypeANY:  mixedAddrs,
		dns.TypeSRV:  mixedAddrs,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	v6v4Qs := map[uint16][]testAddr{
		dns.TypeA:    v6v4Addrs[1:],
		dns.TypeAAAA: v6v4Addrs[:1],
		dns.TypeANY:  v6v4Addrs,
		dns.TypeSRV:  v6v4Addrs,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	instanceQs := map[uint16][]testAddr{
		dns.TypeA:    simpleAddrs[:1],
		dns.TypeAAAA: nil,
		dns.TypeANY:  simpleAddrs[:1],
		dns.TypeSRV:  simpleAddrs[:1],
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	v6instanceQs := map[uint16][]testAddr{
		dns.TypeA:    nil,
		dns.TypeAAAA: v6v4Addrs[:1],
		dns.TypeANY:  v6v4Addrs[:1],
		dns.TypeSRV:  v6v4Addrs[:1],
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	longQs := map[uint16][]testAddr{
		dns.TypeA:    longAddrs,
		dns.TypeAAAA: nil,
		dns.TypeANY:  longAddrs,
		dns.TypeSRV:  longAddrs,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	dupeQs := map[uint16][]testAddr{
		dns.TypeA:    dupeAddrs[1:],
		dns.TypeAAAA: nil,
		dns.TypeANY:  dupeAddrs[1:],
		dns.TypeSRV:  dupeAddrs,
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	leaderQs := map[uint16][]testAddr{
		dns.TypeA:    simpleAddrs[:1],
		dns.TypeAAAA: nil,
		dns.TypeANY:  simpleAddrs[:1],
		dns.TypeSRV:  simpleAddrs[:1],
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}
	v6LeaderQs := map[uint16][]testAddr{
		dns.TypeA:    nil,
		dns.TypeAAAA: v6v4Addrs[:1],
		dns.TypeANY:  v6v4Addrs[:1],
		dns.TypeSRV:  v6v4Addrs[:1],
		dns.TypeSOA:  nil,
		dns.TypeTXT:  nil,
	}

	tests := []test{
		{
			name:   "non-existent service NXDOMAIN",
			domain: "b.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "non-existent service RFC2782 NXDOMAIN",
			domain: "_b._tcp.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "non-existent instance with existing service NXDOMAIN",
			domain: "foo.a._i.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "non-existent instance with non-existent service NXDOMAIN",
			domain: "foo.b._i.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "bad domain NXDOMAIN",
			domain: "asdf.b.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "leader NXDOMAIN",
			domain: "leader.b.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "leader with non-existent service NXDOMAIN",
			domain: "leader.b.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "empty service",
			domain: "a.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "empty 2782",
			domain: "_a._tcp.discoverd.",
			qs:     emptyAll,
		},
		{
			name:   "2782 no proto matches",
			domain: "_a._foo.discoverd.",
			data:   simpleData,
			qs:     emptyAll,
		},
		{
			name:   "2782 full proto matches",
			domain: "_a._tcp.discoverd.",
			data:   simpleData,
			qs:     simpleQs,
		},
		{
			name:   "2782 some proto matches",
			domain: "_a._udp.discoverd.",
			data:   mixedData,
			qs:     udpFiltered,
		},
		{
			name:   "service mixed protocols",
			domain: "a.discoverd.",
			data:   mixedData,
			qs:     mixedQs,
		},
		{
			name:   "service mixed v4/v6",
			domain: "a.discoverd.",
			data:   v6v4Data,
			qs:     v6v4Qs,
		},
		{
			name:   "2782 mixed v4/v6",
			domain: "_a._tcp.discoverd.",
			data:   v6v4Data,
			qs:     v6v4Qs,
		},
		{
			name:   "instance",
			domain: fmt.Sprintf("%s.a._i.discoverd.", simpleData[0].ID),
			data:   simpleData,
			qs:     instanceQs,
		},
		{
			name:   "v6 instance",
			domain: fmt.Sprintf("%s.a._i.discoverd.", v6v4Data[0].ID),
			data:   v6v4Data,
			qs:     v6instanceQs,
		},
		{
			name:   "service udp limit",
			domain: "a.discoverd.",
			data:   longData,
			qs:     longQs,
		},
		{
			name:   "2782 udp limit",
			domain: "_a._tcp.discoverd.",
			data:   longData,
			qs:     longQs,
		},
		{
			name:   "service duplicate IPs",
			domain: "a.discoverd.",
			data:   dupeData,
			qs:     dupeQs,
		},
		{
			name:   "2782 duplicate IPs",
			domain: "_a._tcp.discoverd.",
			data:   dupeData,
			qs:     dupeQs,
		},
		{
			name:   "leader",
			domain: "leader.a.discoverd.",
			data:   simpleData,
			qs:     leaderQs,
		},
		{
			name:   "v6 leader",
			domain: "leader.a.discoverd.",
			data:   v6v4Data[:1],
			qs:     v6LeaderQs,
		},
	}

	// Run all of the tests with TCP as well as UDP
	for i, t := range tests {
		tests[i].net = "udp"
		t.net = "tcp"
		tests = append(tests, t)
	}
	// Run all of the tests again to test case sensitivity
	for _, t := range tests {
		t.domain = strings.ToUpper(t.domain)
		tests = append(tests, t)
	}

	for _, t := range tests {
		func() {
			srv := s.newServer(c, []string{"8.8.8.8", "8.8.4.4"})
			defer srv.Close()
			srv.SetStore(&DNSServerStore{
				InstancesFn: func(service string) ([]*discoverd.Instance, error) {
					if service == "a" {
						if len(t.data) == 0 {
							return []*discoverd.Instance{}, nil
						} else {
							return t.data, nil
						}
					}
					return nil, nil
				},
				ServiceLeaderFn: func(service string) (*discoverd.Instance, error) {
					if service == "a" && len(t.data) > 0 {
						return t.data[0], nil
					}
					return nil, nil
				},
			})

			client := &dns.Client{Net: t.net}
			for q, addrs := range t.qs {
				c.Logf("+ %s: %s - %s - %s", t.domain, t.net, t.name, dns.TypeToString[q])

				// exchange the question
				req := &dns.Msg{}
				req.SetQuestion(t.domain, q)
				addr := srv.UDPAddr
				if t.net == "tcp" {
					addr = srv.TCPAddr
				}
				res, _, err := client.Exchange(req, addr)
				if err != nil && strings.Contains(err.Error(), "i/o timeout") {
					// retry once in case our UDP packet was dropped by the kernel
					res, _, err = client.Exchange(req, addr)
				}
				c.Assert(err, IsNil)

				if strings.Contains(t.name, "NXDOMAIN") {
					// if this is a nxdomain test we just need to ensure we got an nxdomain
					c.Assert(res.Rcode, Equals, dns.RcodeNameError)
					c.Assert(res.Extra, HasLen, 0)
					c.Assert(res.Answer, HasLen, 0)
					assertSOA(c, res.Ns)
					continue
				}

				// UDP responses only include up to three responses
				truncated := t.net == "udp" && len(addrs) > 3

				c.Assert(res.Rcode, Equals, dns.RcodeSuccess)
				switch {
				case q == dns.TypeANY:
					if truncated {
						// three SRV records plus three A/AAAA records
						c.Assert(res.Answer, HasLen, 6)
					} else {
						// SRV + A/AAAA records
						c.Assert(res.Answer, HasLen, len(addrs)*2)
					}
				case q == dns.TypeSOA:
					// the only response to a SOA question should be an SOA answer
					assertSOA(c, res.Answer)
					continue
				case len(addrs) == 0:
					// empty responses include an SOA answer in the authority section
					assertSOA(c, res.Ns)
					c.Assert(res.Answer, HasLen, 0)
				default:
					if truncated {
						c.Assert(res.Answer, HasLen, 3)
					} else {
						c.Assert(res.Answer, HasLen, len(addrs))
					}
				}

				// build a list of all A/AAAA and SRV records received
				ips := make(map[string]struct{}, len(addrs))
				srv := make(map[string]struct{}, len(addrs))
				for _, rr := range res.Answer {
					switch v := rr.(type) {
					case *dns.A:
						ips[v.A.String()] = struct{}{}
						c.Assert(v.A.To4(), NotNil)
						c.Assert(v.Hdr.Name, Equals, t.domain)
						c.Assert(v.Hdr.Rrtype, Equals, dns.TypeA)
					case *dns.AAAA:
						ips[v.AAAA.String()] = struct{}{}
						c.Assert(v.AAAA.To4(), IsNil)
						c.Assert(v.Hdr.Name, Equals, t.domain)
						c.Assert(v.Hdr.Rrtype, Equals, dns.TypeAAAA)
					case *dns.SRV:
						srv[fmt.Sprintf("%s:%d", v.Target, v.Port)] = struct{}{}
						c.Assert(v.Hdr.Name, Equals, t.domain)
						c.Assert(v.Hdr.Rrtype, Equals, dns.TypeSRV)
						c.Assert(v.Weight, Equals, uint16(1))
						c.Assert(v.Priority, Equals, uint16(1))
					default:
						c.Fatalf("unexpected record in answer %#v", v)
					}
				}

				// ensure that we got the expected A/AAAA records
				if q == dns.TypeANY || q == dns.TypeA || q == dns.TypeAAAA {
					if truncated {
						c.Assert(ips, HasLen, 3)
					} else {
						c.Assert(ips, HasLen, len(addrs))
					}

					var found int
					for _, addr := range addrs {
						if _, ok := ips[addr.IP.String()]; ok {
							found++
						}
					}

					if truncated {
						c.Assert(found, Equals, 3)
					} else {
						c.Assert(found, Equals, len(addrs))
					}
				} else {
					c.Assert(ips, HasLen, 0)
				}

				// ensure that we got the expected SRV records
				if q == dns.TypeANY || q == dns.TypeSRV {
					if truncated {
						c.Assert(srv, HasLen, 3)
					} else {
						c.Assert(srv, HasLen, len(addrs))
					}

					if !strings.Contains(t.name, "duplicate") {
						var found int
						for _, addr := range addrs {
							key := fmt.Sprintf("%s.a._i.discoverd.:%d", addr.ID, addr.Port)
							if strings.Contains(t.name, "instance") || strings.Contains(t.name, "leader") {
								key = fmt.Sprintf("%s:%d", t.domain, addr.Port)
							}
							if _, ok := srv[key]; ok {
								found++
							}
						}

						if truncated {
							c.Assert(found, Equals, 3)
						} else {
							c.Assert(found, Equals, len(addrs))
						}
					}
				} else {
					c.Assert(srv, HasLen, 0)
				}

				// responses to SRV questions over TCP get instance records in
				// the extra section so that another lookup is not needed to get
				// the IP addresses
				if t.net == "tcp" && q == dns.TypeSRV {
					c.Assert(res.Extra, HasLen, len(addrs))
					extraIPs := make(map[string]string, len(res.Extra))
					for _, rr := range res.Extra {
						switch v := rr.(type) {
						case *dns.A:
							c.Assert(v.Hdr.Rrtype, Equals, dns.TypeA)
							id := strings.TrimSuffix(strings.ToLower(v.Hdr.Name), ".a._i.discoverd.")
							extraIPs[id] = v.A.String()
						case *dns.AAAA:
							c.Assert(v.Hdr.Rrtype, Equals, dns.TypeAAAA)
							id := strings.TrimSuffix(strings.ToLower(v.Hdr.Name), ".a._i.discoverd.")
							extraIPs[id] = v.AAAA.String()
						default:
							c.Fatalf("unexpected record in extra %#v", v)
						}
					}
					for _, addr := range addrs {
						if strings.Contains(t.name, "leader") {
							c.Assert(extraIPs[strings.ToLower(t.domain)], Equals, addr.IP.String())
						} else {
							c.Assert(extraIPs[addr.ID], Equals, addr.IP.String())
						}
					}
				} else {
					c.Assert(res.Extra, HasLen, 0)
				}
			}
		}()
	}
}

func (s *DNSSuite) TestUserDiscoverdDNSRestricted(c *C) {
	path := filepath.Join(c.MkDir(), "installed-plugins.json")
	c.Assert(os.Setenv("FLYNN_INSTALLED_PLUGINS", path), IsNil)
	c.Assert(plugin.WriteInstalled(path, []plugin.Installed{
		{Name: "mariadb", Datastore: true},
	}), IsNil)
	defer os.Unsetenv("FLYNN_INSTALLED_PLUGINS")

	leader, _ := fakeStaticInstance("tcp", "10.0.0.5", 5432)
	redisLeader, _ := fakeStaticInstance("tcp", "10.0.0.6", 6379)
	isolated, _ := fakeStaticInstance("tcp", "10.0.0.8", 5432)
	appWeb, _ := fakeStaticInstance("tcp", "10.0.0.9", 8080)
	userInst, _ := fakeStaticInstance("tcp", "127.0.0.1", 1)
	redisApp := "redis-11111111-2222-3333-4444-555555555555"

	srv := s.newServer(c, nil)
	defer srv.Close()
	srv.SetStore(&DNSServerStore{
		InstancesFn: func(service string) ([]*discoverd.Instance, error) {
			switch service {
			case "flynn-net-user":
				return []*discoverd.Instance{userInst}, nil
			case "shop-web":
				return []*discoverd.Instance{appWeb}, nil
			case "postgres":
				return []*discoverd.Instance{leader}, nil
			case redisApp:
				return []*discoverd.Instance{redisLeader}, nil
			case "pg-ridge-ffpade":
				return []*discoverd.Instance{isolated}, nil
			default:
				return nil, nil
			}
		},
		ServiceLeaderFn: func(service string) (*discoverd.Instance, error) {
			switch service {
			case "postgres":
				return leader, nil
			case redisApp:
				return redisLeader, nil
			case "mariadb":
				return leader, nil
			case "pg-ridge-ffpade":
				return isolated, nil
			default:
				return nil, nil
			}
		},
	})

	client := &dns.Client{Net: "udp"}
	lookup := func(name string) *dns.Msg {
		req := &dns.Msg{}
		req.SetQuestion(name, dns.TypeA)
		res, _, err := client.Exchange(req, srv.UDPAddr)
		c.Assert(err, IsNil)
		return res
	}

	blocked := lookup("shop-web.discoverd.")
	c.Assert(blocked.Rcode, Equals, dns.RcodeNameError)

	internal := lookup("postgres.discoverd.")
	c.Assert(internal.Rcode, Equals, dns.RcodeNameError)

	api := lookup("postgres-api.discoverd.")
	c.Assert(api.Rcode, Equals, dns.RcodeNameError)

	ok := lookup("leader.postgres.discoverd.")
	c.Assert(ok.Rcode, Equals, dns.RcodeSuccess)
	c.Assert(ok.Answer, Not(HasLen), 0)

	redisInternal := lookup(redisApp + ".discoverd.")
	c.Assert(redisInternal.Rcode, Equals, dns.RcodeNameError)

	redisOK := lookup("leader." + redisApp + ".discoverd.")
	c.Assert(redisOK.Rcode, Equals, dns.RcodeSuccess)
	c.Assert(redisOK.Answer, Not(HasLen), 0)

	pgOK := lookup("leader.pg-ridge-ffpade.discoverd.")
	c.Assert(pgOK.Rcode, Equals, dns.RcodeSuccess)
	c.Assert(pgOK.Answer, Not(HasLen), 0)

	mariadbOK := lookup("leader.mariadb.discoverd.")
	c.Assert(mariadbOK.Rcode, Equals, dns.RcodeSuccess)
	c.Assert(mariadbOK.Answer, Not(HasLen), 0)
}

func (s *DNSSuite) TestUserDiscoverdDNSDatastoreInstanceMeta(c *C) {
	c.Assert(os.Setenv("FLYNN_INSTALLED_PLUGINS", filepath.Join(c.MkDir(), "missing.json")), IsNil)
	defer os.Unsetenv("FLYNN_INSTALLED_PLUGINS")

	leader, _ := fakeStaticInstance("tcp", "10.0.0.5", 3306)
	leader.Meta = map[string]string{"flynn-datastore": "true"}
	bare, _ := fakeStaticInstance("tcp", "10.0.0.7", 3306)
	userInst, _ := fakeStaticInstance("tcp", "127.0.0.1", 1)

	srv := s.newServer(c, nil)
	defer srv.Close()
	srv.SetStore(&DNSServerStore{
		InstancesFn: func(service string) ([]*discoverd.Instance, error) {
			switch service {
			case "flynn-net-user":
				return []*discoverd.Instance{userInst}, nil
			case "mariadb":
				return []*discoverd.Instance{leader}, nil
			case "mongodb":
				return []*discoverd.Instance{bare}, nil
			default:
				return nil, nil
			}
		},
		ServiceLeaderFn: func(service string) (*discoverd.Instance, error) {
			switch service {
			case "mariadb":
				return leader, nil
			case "mongodb":
				return bare, nil
			default:
				return nil, nil
			}
		},
	})

	client := &dns.Client{Net: "udp"}
	lookup := func(name string) *dns.Msg {
		req := &dns.Msg{}
		req.SetQuestion(name, dns.TypeA)
		res, _, err := client.Exchange(req, srv.UDPAddr)
		c.Assert(err, IsNil)
		return res
	}

	ok := lookup("leader.mariadb.discoverd.")
	c.Assert(ok.Rcode, Equals, dns.RcodeSuccess)
	c.Assert(ok.Answer, Not(HasLen), 0)

	blocked := lookup("leader.mongodb.discoverd.")
	c.Assert(blocked.Rcode, Equals, dns.RcodeNameError)
}

func (s *DNSSuite) TestUserDiscoverdDNSAttachedOnly(c *C) {
	attached := "redis-lagoon-59415"
	other := "pg-ridge-ffpade"
	redisLeader, _ := fakeStaticInstance("tcp", "10.0.0.6", 6379)
	pgLeader, _ := fakeStaticInstance("tcp", "10.0.0.8", 5432)
	userInst, _ := fakeStaticInstance("tcp", "127.0.0.1", 1)
	userInst.Meta = map[string]string{netpolicy.MetaDiscoverdAllow: attached}

	srv := s.newServer(c, nil)
	defer srv.Close()
	srv.SetStore(&DNSServerStore{
		InstancesFn: func(service string) ([]*discoverd.Instance, error) {
			switch service {
			case "flynn-net-user":
				return []*discoverd.Instance{userInst}, nil
			case attached:
				return []*discoverd.Instance{redisLeader}, nil
			case other:
				return []*discoverd.Instance{pgLeader}, nil
			default:
				return nil, nil
			}
		},
		ServiceLeaderFn: func(service string) (*discoverd.Instance, error) {
			switch service {
			case attached:
				return redisLeader, nil
			case other:
				return pgLeader, nil
			default:
				return nil, nil
			}
		},
	})

	client := &dns.Client{Net: "udp"}
	lookup := func(name string) *dns.Msg {
		req := &dns.Msg{}
		req.SetQuestion(name, dns.TypeA)
		res, _, err := client.Exchange(req, srv.UDPAddr)
		c.Assert(err, IsNil)
		return res
	}

	ok := lookup("leader." + attached + ".discoverd.")
	c.Assert(ok.Rcode, Equals, dns.RcodeSuccess)
	c.Assert(ok.Answer, Not(HasLen), 0)

	c.Assert(lookup("leader."+other+".discoverd.").Rcode, Equals, dns.RcodeNameError)
	c.Assert(lookup(attached+".discoverd.").Rcode, Equals, dns.RcodeNameError)
	c.Assert(lookup("app-one-web.discoverd.").Rcode, Equals, dns.RcodeNameError)
	c.Assert(lookup("postgres.discoverd.").Rcode, Equals, dns.RcodeNameError)
}

func (s *DNSSuite) TestUserDiscoverdDNSEmptyAllow(c *C) {
	attached := "redis-lagoon-59415"
	redisLeader, _ := fakeStaticInstance("tcp", "10.0.0.6", 6379)
	userInst, _ := fakeStaticInstance("tcp", "127.0.0.1", 1)
	userInst.Meta = map[string]string{netpolicy.MetaDiscoverdAllow: ""}

	srv := s.newServer(c, nil)
	defer srv.Close()
	srv.SetStore(&DNSServerStore{
		InstancesFn: func(service string) ([]*discoverd.Instance, error) {
			switch service {
			case "flynn-net-user":
				return []*discoverd.Instance{userInst}, nil
			case attached:
				return []*discoverd.Instance{redisLeader}, nil
			default:
				return nil, nil
			}
		},
		ServiceLeaderFn: func(service string) (*discoverd.Instance, error) {
			if service == attached {
				return redisLeader, nil
			}
			return nil, nil
		},
	})

	req := &dns.Msg{}
	req.SetQuestion("leader."+attached+".discoverd.", dns.TypeA)
	res, _, err := (&dns.Client{Net: "udp"}).Exchange(req, srv.UDPAddr)
	c.Assert(err, IsNil)
	c.Assert(res.Rcode, Equals, dns.RcodeNameError)
}

// localJobsFunc is a fake LocalJobs that counts its calls.
type localJobsFunc struct {
	fn    func(ip net.IP) (*discoverd.Instance, error)
	calls int32
}

func (f *localJobsFunc) OverlayInstance(ip net.IP) (*discoverd.Instance, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.fn(ip)
}

func mustCIDR(c *C, s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	c.Assert(err, IsNil)
	return n
}

// newOverlayServer is a DNS server whose test client (127.0.0.1) is an
// overlay IP. netSvc maps a netpolicy service to the instances listed in it;
// postgres and redisApp are system/datastore services with a leader.
func (s *DNSSuite) newOverlayServer(c *C, overlay, local string, jobs LocalJobs, netSvc map[string][]*discoverd.Instance, self ...net.IP) (*DNSServer, func(string) int) {
	pg, _ := fakeStaticInstance("tcp", "10.0.0.5", 5432)
	redis, _ := fakeStaticInstance("tcp", "10.0.0.6", 6379)
	other, _ := fakeStaticInstance("tcp", "10.0.0.8", 6379)
	srv := &DNSServer{UDPAddr: "127.0.0.1:0", TCPAddr: "127.0.0.1:0", LocalJobs: jobs, SelfIPs: self}
	if overlay != "" {
		srv.Overlay = mustCIDR(c, overlay)
	}
	if local != "" {
		srv.LocalSubnet = mustCIDR(c, local)
	}
	srv.SetStore(&DNSServerStore{
		InstancesFn: func(service string) ([]*discoverd.Instance, error) {
			if insts, ok := netSvc[service]; ok {
				return insts, nil
			}
			switch service {
			case "postgres":
				return []*discoverd.Instance{pg}, nil
			case "redis-lagoon-59415":
				return []*discoverd.Instance{redis}, nil
			case "redis-victim-11111":
				return []*discoverd.Instance{other}, nil
			}
			return nil, nil
		},
		ServiceLeaderFn: func(service string) (*discoverd.Instance, error) {
			switch service {
			case "postgres":
				return pg, nil
			case "redis-lagoon-59415":
				return redis, nil
			case "redis-victim-11111":
				return other, nil
			}
			return nil, nil
		},
	})
	c.Assert(srv.ListenAndServe(), IsNil)
	lookup := func(name string) int {
		req := &dns.Msg{}
		req.SetQuestion(name, dns.TypeA)
		res, _, err := (&dns.Client{Net: "udp"}).Exchange(req, srv.UDPAddr)
		c.Assert(err, IsNil)
		return res.Rcode
	}
	return srv, lookup
}

// CORE-25-F1: an overlay IP that is in no netpolicy service is not a system
// client. Before the fix it got system answers, so a user job could resolve
// another tenant's datastore leader until its own registration landed.
func (s *DNSSuite) TestDNSUnregisteredOverlayClientFailsClosed(c *C) {
	srv, lookup := s.newOverlayServer(c, "127.0.0.0/8", "", nil, nil)
	defer srv.Close()

	c.Assert(lookup("leader.redis-victim-11111.discoverd."), Equals, dns.RcodeNameError)
	c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeNameError)
	c.Assert(lookup("postgres.discoverd."), Equals, dns.RcodeNameError)
}

// A local job whose registration has not reached this discoverd yet gets the
// answer for the registration flynn-host holds: its own allowlist for a user
// job, system answers for a system job.
func (s *DNSSuite) TestDNSUnregisteredLocalClientUsesHostRegistration(c *C) {
	user := &localJobsFunc{fn: func(ip net.IP) (*discoverd.Instance, error) {
		c.Assert(ip.String(), Equals, "127.0.0.1")
		return &discoverd.Instance{Meta: map[string]string{
			"class": "user", netpolicy.MetaDiscoverdAllow: "redis-lagoon-59415",
		}}, nil
	}}
	srv, lookup := s.newOverlayServer(c, "127.0.0.0/8", "127.0.0.0/24", user, nil)
	c.Assert(lookup("leader.redis-lagoon-59415.discoverd."), Equals, dns.RcodeSuccess)
	c.Assert(lookup("leader.redis-victim-11111.discoverd."), Equals, dns.RcodeNameError)
	c.Assert(lookup("postgres.discoverd."), Equals, dns.RcodeNameError)
	c.Assert(atomic.LoadInt32(&user.calls) > 0, Equals, true)
	srv.Close()

	for _, class := range []string{"system", "datastore", "build"} {
		sys := &localJobsFunc{fn: func(net.IP) (*discoverd.Instance, error) {
			return &discoverd.Instance{Meta: map[string]string{"class": class}}, nil
		}}
		srv, lookup = s.newOverlayServer(c, "127.0.0.0/8", "127.0.0.0/24", sys, nil)
		c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeSuccess, Commentf("class %s", class))
		c.Assert(lookup("postgres.discoverd."), Equals, dns.RcodeSuccess, Commentf("class %s", class))
		srv.Close()
	}
}

// No job owns the IP, or flynn-host cannot be asked: fail closed.
func (s *DNSSuite) TestDNSUnregisteredLocalClientUnknownToHostFailsClosed(c *C) {
	for _, jobs := range []*localJobsFunc{
		{fn: func(net.IP) (*discoverd.Instance, error) { return nil, nil }},
		{fn: func(net.IP) (*discoverd.Instance, error) { return nil, fmt.Errorf("host API down") }},
		{fn: func(net.IP) (*discoverd.Instance, error) {
			return &discoverd.Instance{Meta: map[string]string{}}, nil // no class
		}},
		{fn: func(net.IP) (*discoverd.Instance, error) {
			// user without the allowlist key: not the legacy env-wide answer
			return &discoverd.Instance{Meta: map[string]string{"class": "user"}}, nil
		}},
	} {
		srv, lookup := s.newOverlayServer(c, "127.0.0.0/8", "127.0.0.0/24", jobs, nil)
		c.Assert(lookup("leader.redis-lagoon-59415.discoverd."), Equals, dns.RcodeNameError)
		c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeNameError)
		srv.Close()
	}
}

// Registered overlay IPs keep their answers and never cost a host lookup.
func (s *DNSSuite) TestDNSRegisteredOverlayClientSkipsHostLookup(c *C) {
	self, _ := fakeStaticInstance("tcp", "127.0.0.1", 1)
	for _, svc := range []string{netpolicy.ServiceSys, netpolicy.ServiceData, netpolicy.ServiceBuild} {
		jobs := &localJobsFunc{fn: func(net.IP) (*discoverd.Instance, error) { return nil, nil }}
		srv, lookup := s.newOverlayServer(c, "127.0.0.0/8", "127.0.0.0/24", jobs,
			map[string][]*discoverd.Instance{svc: {self}})
		c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeSuccess, Commentf("service %s", svc))
		c.Assert(lookup("postgres.discoverd."), Equals, dns.RcodeSuccess, Commentf("service %s", svc))
		c.Assert(atomic.LoadInt32(&jobs.calls), Equals, int32(0))
		srv.Close()
	}

	userSelf, _ := fakeStaticInstance("tcp", "127.0.0.1", 1)
	userSelf.Meta = map[string]string{netpolicy.MetaDiscoverdAllow: "redis-lagoon-59415"}
	jobs := &localJobsFunc{fn: func(net.IP) (*discoverd.Instance, error) {
		return &discoverd.Instance{Meta: map[string]string{"class": "system"}}, nil
	}}
	srv, lookup := s.newOverlayServer(c, "127.0.0.0/8", "127.0.0.0/24", jobs,
		map[string][]*discoverd.Instance{netpolicy.ServiceUser: {userSelf}})
	defer srv.Close()
	c.Assert(lookup("leader.redis-lagoon-59415.discoverd."), Equals, dns.RcodeSuccess)
	c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeNameError)
	c.Assert(atomic.LoadInt32(&jobs.calls), Equals, int32(0))
}

// Host-network jobs use the host's resolv.conf, whose nameserver is the
// bridge address, so their queries come from the bridge address itself. That
// is inside the overlay, but it is the host, not a job.
func (s *DNSSuite) TestDNSHostBridgeAddressIsSystem(c *C) {
	jobs := &localJobsFunc{fn: func(net.IP) (*discoverd.Instance, error) { return nil, nil }}
	srv, lookup := s.newOverlayServer(c, "127.0.0.0/8", "127.0.0.0/24", jobs, nil, net.ParseIP("127.0.0.1"))
	defer srv.Close()
	c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeSuccess)
	c.Assert(lookup("postgres.discoverd."), Equals, dns.RcodeSuccess)
	c.Assert(atomic.LoadInt32(&jobs.calls), Equals, int32(0))
}

// Host-network jobs and the host itself query from underlay or loopback
// addresses. Those stay system clients.
func (s *DNSSuite) TestDNSClientOutsideOverlayIsSystem(c *C) {
	jobs := &localJobsFunc{fn: func(net.IP) (*discoverd.Instance, error) { return nil, nil }}
	srv, lookup := s.newOverlayServer(c, "100.100.0.0/16", "100.100.7.0/24", jobs, nil)
	defer srv.Close()
	c.Assert(lookup("leader.postgres.discoverd."), Equals, dns.RcodeSuccess)
	c.Assert(lookup("postgres.discoverd."), Equals, dns.RcodeSuccess)
	c.Assert(atomic.LoadInt32(&jobs.calls), Equals, int32(0))
}

func assertSOA(c *C, rrs []dns.RR) {
	c.Assert(rrs, HasLen, 1)
	c.Assert(rrs[0], FitsTypeOf, &dns.SOA{})
	soa := rrs[0].(*dns.SOA)
	c.Assert(soa.Hdr.Name, Equals, "discoverd.")
	c.Assert(soa.Hdr.Rrtype, Equals, dns.TypeSOA)
}

var dnsIndex uint64

func fakeStaticInstance(proto, ip string, port uint16) (*discoverd.Instance, testAddr) {
	inst := &discoverd.Instance{
		Proto: proto,
		Addr:  net.JoinHostPort(ip, strconv.Itoa(int(port))),
		Index: atomic.AddUint64(&dnsIndex, 1),
	}

	digest := md5.Sum([]byte(inst.Proto + "-" + inst.Addr))
	inst.ID = hex.EncodeToString(digest[:])
	netIP := net.ParseIP(ip)
	return inst, testAddr{netIP, port, inst.ID}
}

type testAddr struct {
	IP   net.IP
	Port uint16
	ID   string
}

// DNSServerStore represents a mock implementation of DNSServer.Store.
type DNSServerStore struct {
	InstancesFn     func(service string) ([]*discoverd.Instance, error)
	ServiceLeaderFn func(service string) (*discoverd.Instance, error)
}

func (s *DNSServerStore) Instances(service string) ([]*discoverd.Instance, error) {
	return s.InstancesFn(service)
}

func (s *DNSServerStore) ServiceLeader(service string) (*discoverd.Instance, error) {
	return s.ServiceLeaderFn(service)
}

// fsmStore is a Store that is fed raft log entries directly, without raft.
type fsmStore struct {
	*Store
	index uint64
}

func newFSMStore(tb testing.TB) *fsmStore {
	tb.Helper()
	return &fsmStore{Store: NewStore(tb.TempDir())}
}

func (s *fsmStore) apply(tb testing.TB, typ byte, cmd interface{}) {
	tb.Helper()
	buf, err := json.Marshal(cmd)
	if err != nil {
		tb.Fatal(err)
	}
	s.index++
	if res := s.Apply(&raft.Log{Index: s.index, Data: append([]byte{typ}, buf...)}); res != nil {
		if err, ok := res.(error); ok {
			tb.Fatal(err)
		}
	}
}

// tryApply applies cmd and ignores the command's own error.
func (s *fsmStore) tryApply(tb testing.TB, typ byte, cmd interface{}) {
	tb.Helper()
	buf, err := json.Marshal(cmd)
	if err != nil {
		tb.Fatal(err)
	}
	s.index++
	s.Apply(&raft.Log{Index: s.index, Data: append([]byte{typ}, buf...)})
}

func (s *fsmStore) addService(tb testing.TB, service string) {
	s.apply(tb, addServiceCommandType, &addServiceCommand{Service: service, Config: DefaultServiceConfig})
}

func (s *fsmStore) addInstance(tb testing.TB, service string, inst *discoverd.Instance) {
	s.apply(tb, addInstanceCommandType, &addInstanceCommand{Service: service, Instance: inst})
}

func (s *fsmStore) removeInstance(tb testing.TB, service, id string) {
	s.apply(tb, removeInstanceCommandType, &removeInstanceCommand{Service: service, ID: id})
}

func (s *fsmStore) expireInstance(tb testing.TB, service, id string) {
	s.apply(tb, expireInstancesCommandType, &expireInstancesCommand{Instances: []expireInstance{{Service: service, InstanceID: id}}})
}

// netUserInst is a flynn-net-user registration for ip with an allowlist.
func netUserInst(id, ip, allow string) *discoverd.Instance {
	return &discoverd.Instance{
		ID:    id,
		Addr:  net.JoinHostPort(ip, "0"),
		Proto: "tcp",
		Meta:  map[string]string{"class": "user", netpolicy.MetaDiscoverdAllow: allow},
	}
}

// manyUserJobsStore registers n user jobs (none of them 127.0.0.1) plus a
// postgres leader and a redis datastore leader.
func manyUserJobsStore(tb testing.TB, n int) *fsmStore {
	tb.Helper()
	s := newFSMStore(tb)
	for _, svc := range []string{netpolicy.ServiceUser, netpolicy.ServiceSys, netpolicy.ServiceData, netpolicy.ServiceBuild, "postgres", "redis-lagoon-59415"} {
		s.addService(tb, svc)
	}
	for i := 0; i < n; i++ {
		ip := fmt.Sprintf("100.100.%d.%d", 1+i/250, 1+i%250)
		s.addInstance(tb, netpolicy.ServiceUser, netUserInst(fmt.Sprintf("user%d", i), ip, "redis-other"))
	}
	s.addInstance(tb, "postgres", &discoverd.Instance{ID: "pg0", Addr: "10.0.0.5:5432", Proto: "tcp"})
	s.addInstance(tb, "redis-lagoon-59415", &discoverd.Instance{ID: "redis0", Addr: "10.0.0.6:6379", Proto: "tcp"})
	return s
}

// countingDNSStore counts the Instances calls the DNS server makes.
type countingDNSStore struct {
	*fsmStore
	mu    sync.Mutex
	calls map[string]int
}

func (s *countingDNSStore) Instances(service string) ([]*discoverd.Instance, error) {
	s.mu.Lock()
	s.calls[service]++
	s.mu.Unlock()
	return s.fsmStore.Instances(service)
}

func (s *countingDNSStore) netPolicyCalls() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := make(map[string]int)
	for svc, n := range s.calls {
		if strings.HasPrefix(svc, "flynn-net-") {
			res[svc] = n
		}
	}
	return res
}

// CORE-41: the DNS server finds the client's netpolicy registration without
// listing (and so copying and sorting) every instance of the netpolicy
// services. Before the fix each *.discoverd query called
// Instances("flynn-net-user"), and a system client also listed the other
// three services.
func TestUserNetLookupDoesNotCopyInstances(t *testing.T) {
	fsm := manyUserJobsStore(t, 500)
	store := &countingDNSStore{fsmStore: fsm, calls: make(map[string]int)}
	srv := &DNSServer{UDPAddr: "127.0.0.1:0", Overlay: &net.IPNet{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)}}
	srv.SetStore(store)
	if err := srv.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	lookup := func(name string) int {
		t.Helper()
		req := &dns.Msg{}
		req.SetQuestion(name, dns.TypeA)
		res, _, err := (&dns.Client{Net: "udp"}).Exchange(req, srv.UDPAddr)
		if err != nil {
			t.Fatal(err)
		}
		return res.Rcode
	}

	// A user job (the test client, 127.0.0.1) gets its allowlist.
	fsm.addInstance(t, netpolicy.ServiceUser, netUserInst("me", "127.0.0.1", "redis-lagoon-59415"))
	if rc := lookup("leader.redis-lagoon-59415.discoverd."); rc != dns.RcodeSuccess {
		t.Fatalf("user job: attached leader rcode %d, want success", rc)
	}
	if rc := lookup("leader.postgres.discoverd."); rc != dns.RcodeNameError {
		t.Fatalf("user job: postgres leader rcode %d, want NXDOMAIN", rc)
	}

	// The same IP re-registered as a system job gets every answer.
	fsm.removeInstance(t, netpolicy.ServiceUser, "me")
	fsm.addInstance(t, netpolicy.ServiceSys, &discoverd.Instance{ID: "sys", Addr: "127.0.0.1:0", Proto: "tcp", Meta: map[string]string{"class": "system"}})
	if rc := lookup("postgres.discoverd."); rc != dns.RcodeSuccess {
		t.Fatalf("system job: postgres rcode %d, want success", rc)
	}

	// Gone from every netpolicy service: an unknown overlay client fails closed.
	fsm.expireInstance(t, netpolicy.ServiceSys, "sys")
	if rc := lookup("postgres.discoverd."); rc != dns.RcodeNameError {
		t.Fatalf("unregistered overlay client: postgres rcode %d, want NXDOMAIN", rc)
	}

	if calls := store.netPolicyCalls(); len(calls) != 0 {
		t.Fatalf("DNS lookups listed netpolicy services: %v", calls)
	}
}

// fakeDNSWriter is a dns.ResponseWriter for calling ServiceLookup directly.
type fakeDNSWriter struct {
	remote net.Addr
	msg    *dns.Msg
}

func (w *fakeDNSWriter) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
}
func (w *fakeDNSWriter) RemoteAddr() net.Addr      { return w.remote }
func (w *fakeDNSWriter) WriteMsg(m *dns.Msg) error { w.msg = m; return nil }
func (w *fakeDNSWriter) Write([]byte) (int, error) { return 0, nil }
func (w *fakeDNSWriter) Close() error              { return nil }
func (w *fakeDNSWriter) TsigStatus() error         { return nil }
func (w *fakeDNSWriter) TsigTimersOnly(bool)       {}
func (w *fakeDNSWriter) Hijack()                   {}

// BenchmarkServiceLookupManyUserJobs measures one *.discoverd lookup from a
// user job and from a system job while n user jobs are registered.
func BenchmarkServiceLookupManyUserJobs(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		store := manyUserJobsStore(b, n)
		store.addInstance(b, netpolicy.ServiceUser, netUserInst("me", "100.101.0.1", "redis-lagoon-59415"))
		store.addInstance(b, netpolicy.ServiceSys, &discoverd.Instance{ID: "sys", Addr: "100.101.0.2:0", Proto: "tcp"})
		srv := &DNSServer{Domain: dnsDomain, Overlay: &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}}
		srv.SetStore(store.Store)
		api := dnsAPI{srv}
		for _, client := range []struct{ name, ip, query string }{
			{"user", "100.101.0.1", "leader.redis-lagoon-59415.discoverd."},
			{"system", "100.101.0.2", "postgres.discoverd."},
		} {
			b.Run(fmt.Sprintf("%s/n=%d", client.name, n), func(b *testing.B) {
				req := &dns.Msg{}
				req.SetQuestion(client.query, dns.TypeA)
				w := &fakeDNSWriter{remote: &net.UDPAddr{IP: net.ParseIP(client.ip), Port: 5353}}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					api.ServiceLookup(w, req)
					if w.msg.Rcode != dns.RcodeSuccess {
						b.Fatalf("rcode %d", w.msg.Rcode)
					}
				}
			})
		}
	}
}
