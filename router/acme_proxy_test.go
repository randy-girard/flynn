package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/flynn/go-check"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/netpolicy"
	"github.com/randy-girard/flynn/pkg/stream"
)

func acmeStatus(c *C, url string) (int, string) {
	res, err := http.Get(url)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body)
}

func waitACMEStatus(c *C, url string, want int, wantBody string) {
	deadline := time.Now().Add(waitTimeout)
	for {
		code, body := acmeStatus(c, url)
		if code == want && (wantBody == "" || body == wantBody) {
			return
		}
		if time.Now().After(deadline) {
			c.Fatalf("GET %s = %d %q, want %d %q", url, code, body, want, wantBody)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// EDGE-01: an acme-challenge instance on a user (tenant) overlay IP must
// never receive the cluster's HTTP-01 challenge traffic.
func (s *S) TestACMEChallengeSkipsUserJobInstances(c *C) {
	srv := httptest.NewServer(httpTestHandler("acme"))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	ip, _, err := net.SplitHostPort(addr)
	c.Assert(err, IsNil)

	userHB, err := s.discoverd.AddServiceAndRegister(netpolicy.ServiceUser, net.JoinHostPort(ip, netpolicy.DummyPort))
	c.Assert(err, IsNil)
	_, err = s.discoverd.AddServiceAndRegister("acme-challenge", addr)
	c.Assert(err, IsNil)

	l := s.newHTTPListener(c)
	defer l.Close()
	url := "http://" + l.Addrs[0] + acmeChallengePath + "token"

	waitACMEStatus(c, url, 503, "")
	// it must stay refused, not race to the tenant instance
	for i := 0; i < 5; i++ {
		code, _ := acmeStatus(c, url)
		c.Assert(code, Equals, 503)
	}

	c.Assert(userHB.Close(), IsNil)
	waitACMEStatus(c, url, 200, "acme")
}

var acmeTestClient = &http.Client{Timeout: 5 * time.Second}

// acmeGet is acmeStatus with a client timeout, so a router that hangs on a
// stuck responder fails the test instead of hanging it.
func acmeGet(url string) (int, string, error) {
	res, err := acmeTestClient.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), nil
}

func (s *S) startACMEListener(c *C, d DiscoverdClient, setup func(*HTTPListener)) (*HTTPListener, string) {
	l := s.buildHTTPListener(c)
	if d != nil {
		l.discoverd = d
	}
	if setup != nil {
		setup(l)
	}
	c.Assert(l.Start(), IsNil)
	l.defaultPorts = getDefaultPortsFromAddrs(l)
	return l, "http://" + l.Addrs[0] + acmeChallengePath + "token"
}

func waitACMEInstances(c *C, l *HTTPListener, url string, n int) {
	deadline := time.Now().Add(waitTimeout)
	for {
		acmeGet(url)
		l.mtx.RLock()
		sc := l.acmeService
		l.mtx.RUnlock()
		if sc != nil && len(sc.Instances()) == n {
			return
		}
		if time.Now().After(deadline) {
			c.Fatalf("acme-challenge never reached %d instances", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// deadAddr returns a loopback address nothing listens on.
func deadAddr(c *C) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// EDGE-21: a challenge set on one responder must be answered even when
// another responder (a deploy's old worker, a dead registration) is listed.
func (s *S) TestACMEChallengeTriesEveryInstance(c *C) {
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	good := httptest.NewServer(httpTestHandler("keyauth"))
	defer good.Close()

	_, err := s.discoverd.AddServiceAndRegister(acmeChallengeService, missing.Listener.Addr().String())
	c.Assert(err, IsNil)
	_, err = s.discoverd.AddServiceAndRegister(acmeChallengeService, deadAddr(c))
	c.Assert(err, IsNil)
	goodHB, err := s.discoverd.AddServiceAndRegister(acmeChallengeService, good.Listener.Addr().String())
	c.Assert(err, IsNil)

	l, url := s.startACMEListener(c, nil, nil)
	defer l.Close()
	waitACMEInstances(c, l, url, 3)
	for i := 0; i < 20; i++ {
		code, body, err := acmeGet(url)
		c.Assert(err, IsNil)
		c.Assert(code, Equals, 200, Commentf("attempt %d body %q", i, body))
		c.Assert(body, Equals, "keyauth")
	}

	// No responder has the token: the answer is the responder's 404, not
	// the dead instance's 502.
	c.Assert(goodHB.Close(), IsNil)
	waitACMEInstances(c, l, url, 2)
	for i := 0; i < 10; i++ {
		code, _, err := acmeGet(url)
		c.Assert(err, IsNil)
		c.Assert(code, Equals, 404, Commentf("attempt %d", i))
	}
}

// EDGE-21: a responder that accepts and never answers must not hold the
// challenge request; the router moves on to the next instance.
func (s *S) TestACMEChallengeInstanceTimeout(c *C) {
	stop := make(chan struct{})
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-stop:
		case <-req.Context().Done():
		}
	}))
	defer stuck.Close()
	defer close(stop)
	good := httptest.NewServer(httpTestHandler("keyauth"))
	defer good.Close()

	_, err := s.discoverd.AddServiceAndRegister(acmeChallengeService, stuck.Listener.Addr().String())
	c.Assert(err, IsNil)
	_, err = s.discoverd.AddServiceAndRegister(acmeChallengeService, good.Listener.Addr().String())
	c.Assert(err, IsNil)

	l, url := s.startACMEListener(c, nil, func(l *HTTPListener) { l.acmeTimeout = 300 * time.Millisecond })
	defer l.Close()
	waitACMEInstances(c, l, url, 2)
	for i := 0; i < 5; i++ {
		start := time.Now()
		code, body, err := acmeGet(url)
		c.Assert(err, IsNil, Commentf("attempt %d", i))
		c.Assert(code, Equals, 200)
		c.Assert(body, Equals, "keyauth")
		c.Assert(time.Since(start) < 2*time.Second, Equals, true)
	}
}

// acmeWatchDiscoverd counts and can fail or stall the router's watches on
// the acme-challenge service.
type acmeWatchDiscoverd struct {
	DiscoverdClient
	mtx     sync.Mutex
	watches int
	err     error
	stall   chan struct{}
}

func (d *acmeWatchDiscoverd) Service(name string) discoverd.Service {
	svc := d.DiscoverdClient.Service(name)
	if name != acmeChallengeService {
		return svc
	}
	return &acmeWatchService{Service: svc, d: d}
}

func (d *acmeWatchDiscoverd) set(err error, stall chan struct{}) {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	d.err, d.stall = err, stall
}

func (d *acmeWatchDiscoverd) count() int {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	return d.watches
}

type acmeWatchService struct {
	discoverd.Service
	d *acmeWatchDiscoverd
}

func (s *acmeWatchService) Watch(ch chan *discoverd.Event) (stream.Stream, error) {
	s.d.mtx.Lock()
	s.d.watches++
	err, stall := s.d.err, s.d.stall
	s.d.mtx.Unlock()
	if stall != nil {
		<-stall
	}
	if err != nil {
		return nil, err
	}
	return s.Service.Watch(ch)
}

// EDGE-21: while the acme-challenge watch cannot start, challenge requests
// (unauthenticated, any host) must not each open a new discoverd watch.
func (s *S) TestACMEChallengeMissingServiceIsCached(c *C) {
	d := &acmeWatchDiscoverd{DiscoverdClient: s.discoverd}
	d.set(errors.New("service unavailable"), nil)
	l, url := s.startACMEListener(c, d, func(l *HTTPListener) { l.acmeRetryDelay = time.Second })
	defer l.Close()

	for i := 0; i < 20; i++ {
		code, _, err := acmeGet(url)
		c.Assert(err, IsNil)
		c.Assert(code, Equals, 503)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _, err := acmeGet(url)
			c.Check(err, IsNil)
			c.Check(code, Equals, 503)
		}()
	}
	wg.Wait()
	c.Assert(d.count(), Equals, 1)

	// After the retry delay the watch is tried again and then kept.
	d.set(nil, nil)
	good := httptest.NewServer(httpTestHandler("keyauth"))
	defer good.Close()
	_, err := s.discoverd.AddServiceAndRegister(acmeChallengeService, good.Listener.Addr().String())
	c.Assert(err, IsNil)
	waitACMEStatus(c, url, 200, "keyauth")
	n := d.count()
	c.Assert(n >= 2, Equals, true)
	for i := 0; i < 10; i++ {
		code, _, err := acmeGet(url)
		c.Assert(err, IsNil)
		c.Assert(code, Equals, 200)
	}
	c.Assert(d.count(), Equals, n)
}

// EDGE-21: a discoverd that accepts the watch but stalls must not pile up
// one blocked watch per challenge request, and each request gets an answer.
func (s *S) TestACMEChallengeStalledWatchIsShared(c *C) {
	stall := make(chan struct{})
	d := &acmeWatchDiscoverd{DiscoverdClient: s.discoverd}
	d.set(errors.New("gave up"), stall)
	l, url := s.startACMEListener(c, d, func(l *HTTPListener) { l.acmeTimeout = 300 * time.Millisecond })
	defer l.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			code, _, err := acmeGet(url)
			c.Check(err, IsNil)
			c.Check(code, Equals, 503)
			c.Check(time.Since(start) < 2*time.Second, Equals, true)
		}()
	}
	wg.Wait()
	c.Assert(d.count(), Equals, 1)
	close(stall)
}
