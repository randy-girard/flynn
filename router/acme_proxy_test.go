package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/pkg/netpolicy"
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
