package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"strconv"
	"time"

	. "github.com/flynn/go-check"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/testutil"
	"github.com/randy-girard/flynn/pkg/keepalive"
	"github.com/randy-girard/flynn/pkg/tlscert"
	"github.com/randy-girard/flynn/router/testutils"
	router "github.com/randy-girard/flynn/router/types"
)

func NewTCPTestServer(prefix string) *TCPTestServer {
	s := &TCPTestServer{prefix: prefix}
	var err error
	s.l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s.Addr = s.l.Addr().String()
	go s.Serve()
	return s
}

type TCPTestServer struct {
	Addr   string
	prefix string
	l      net.Listener
}

func (s *TCPTestServer) Serve() {
	for {
		conn, err := s.l.Accept()
		if err != nil {
			return
		}
		go func() {
			conn.Write([]byte(s.prefix))
			io.Copy(conn, conn)
			conn.Close()
		}()
	}
}

func (s *TCPTestServer) Close() error { return s.l.Close() }

func (s *S) newTCPListener(t testutil.TestingT) *TCPListener {
	l := &TCPListener{
		IP:        "127.0.0.1",
		syncer:    NewSyncer(s.store, "tcp"),
		discoverd: s.discoverd,
	}
	l.startPort, l.endPort = allocatePortRange(10)
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}

	return l
}

func assertTCPConn(c *C, addr, prefix string) {
	conn, err := net.Dial("tcp", addr)
	c.Assert(err, IsNil)
	conn.Write([]byte("asdf"))
	conn.(*net.TCPConn).CloseWrite()
	res, err := ioutil.ReadAll(conn)
	conn.Close()

	c.Assert(err, IsNil)
	c.Assert(string(res), Equals, prefix+"asdf")
}

func (s *S) TestTCPRouteOutsidePrebindListensOnDemand(c *C) {
	l := s.newTCPListener(c)
	defer l.Close()
	port := l.endPort + 1

	l.mtx.RLock()
	prebind := len(l.listeners)
	_, held := l.listeners[port]
	l.mtx.RUnlock()
	c.Assert(held, Equals, false)
	c.Assert(prebind > 0, Equals, true)

	srv := NewTCPTestServer("ondemand")
	defer srv.Close()
	r := s.addTCPRoute(c, l, port)
	unregister := discoverdRegisterTCP(c, l, srv.Addr)
	assertTCPConn(c, fmt.Sprintf("127.0.0.1:%d", port), "ondemand")
	unregister()

	l.mtx.RLock()
	_, inPool := l.listeners[port]
	l.mtx.RUnlock()
	c.Assert(inPool, Equals, false)

	wait := waitForEvent(c, l, "remove", r.ID)
	s.store.delete(r.ToRoute())
	wait()

	l.mtx.RLock()
	_, returned := l.listeners[port]
	left := len(l.listeners)
	l.mtx.RUnlock()
	c.Assert(returned, Equals, false)
	c.Assert(left, Equals, prebind)
	_, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 250*time.Millisecond)
	c.Assert(err, NotNil)
}

func (s *S) TestAddTCPRoute(c *C) {
	portInt := allocatePort()
	port := strconv.Itoa(portInt)
	addr := "127.0.0.1:" + port

	srv1 := NewTCPTestServer("1")
	srv2 := NewTCPTestServer("2")
	defer srv1.Close()
	defer srv2.Close()

	l := s.newTCPListener(c)
	defer l.Close()

	r := s.addTCPRoute(c, l, portInt)

	unregister := discoverdRegisterTCP(c, l, srv1.Addr)

	assertTCPConn(c, addr, "1")

	unregister()
	discoverdRegisterTCP(c, l, srv2.Addr)

	assertTCPConn(c, addr, "2")

	wait := waitForEvent(c, l, "remove", r.ID)
	s.store.delete(r.ToRoute())
	wait()

	_, err := net.Dial("tcp", addr)
	c.Assert(err, Not(IsNil))
}

func (s *S) addTCPRoute(c *C, l *TCPListener, port int) *router.TCPRoute {
	wait := waitForEvent(c, l, "set", "")
	r := router.TCPRoute{
		Service: "test",
		Port:    port,
	}.ToRoute()
	s.store.add(r)
	wait()
	return r.TCPRoute()
}

func (s *S) TestTCPLeaderRouting(c *C) {
	portInt := allocatePort()
	port := strconv.Itoa(portInt)
	addr := "127.0.0.1:" + port

	srv1 := NewTCPTestServer("1")
	srv2 := NewTCPTestServer("2")
	defer srv1.Close()
	defer srv2.Close()

	l := s.newTCPListener(c)
	defer l.Close()

	client := l.discoverd
	err := client.AddService("leader-routing-tcp", &discoverd.ServiceConfig{
		LeaderType: discoverd.LeaderTypeManual,
	})
	c.Assert(err, IsNil)

	wait := waitForEvent(c, l, "set", "")
	r := router.TCPRoute{
		Service: "leader-routing-tcp",
		Port:    portInt,
		Leader:  true,
	}.ToRoute()
	s.store.add(r)
	wait()

	discoverdRegisterTCPService(c, l, "leader-routing-tcp", srv1.Addr)
	discoverdRegisterTCPService(c, l, "leader-routing-tcp", srv2.Addr)

	discoverdSetLeaderTCP(c, l, "leader-routing-tcp", md5sum("tcp-"+srv1.Addr))
	assertTCPConn(c, addr, "1")

	discoverdSetLeaderTCP(c, l, "leader-routing-tcp", md5sum("tcp-"+srv2.Addr))
	assertTCPConn(c, addr, "2")
}

func (s *S) TestTCPTLSTerminate(c *C) {
	portInt := allocatePort()
	addr := fmt.Sprintf("127.0.0.1:%d", portInt)

	srv := NewTCPTestServer("tls-ok")
	defer srv.Close()

	l := s.newTCPListener(c)
	defer l.Close()

	cert := testutils.TLSConfigForDomain("postgres.example.com")
	wait := waitForEvent(c, l, "set", "")
	r := router.TCPRoute{
		Service: "tls-tcp",
		Port:    portInt,
		Domain:  "postgres.example.com",
		TLSMode: router.TLSModeTerminate,
		Certificate: &router.Certificate{
			Cert: cert.Cert,
			Key:  cert.PrivateKey,
		},
	}.ToRoute()
	s.store.add(r)
	wait()

	unregister := discoverdRegisterTCPService(c, l, "tls-tcp", srv.Addr)
	defer unregister()

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	c.Assert(err, IsNil)
	conn.Write([]byte("ping"))
	conn.CloseWrite()
	res, err := ioutil.ReadAll(conn)
	conn.Close()
	c.Assert(err, IsNil)
	c.Assert(string(res), Equals, "tls-okping")
}

func (s *S) TestInitialTCPSync(c *C) {
	port := allocatePort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	l := s.newTCPListener(c)
	s.addTCPRoute(c, l, port)
	l.Close()

	srv := NewTCPTestServer("1")
	defer srv.Close()

	l = s.newTCPListener(c)
	defer l.Close()

	discoverdRegisterTCP(c, l, srv.Addr)

	assertTCPConn(c, addr, "1")
}

// useReusePortListen makes the listener bind with SO_REUSEPORT like the real
// router does, so a second bind on a served port succeeds instead of failing.
func useReusePortListen() func() {
	prev := listenFunc
	listenFunc = keepalive.ReusableListen
	return func() { listenFunc = prev }
}

func (s *S) addTCPRouteToStore(c *C, l *TCPListener, store *testStore, r *router.Route) *router.Route {
	wait := waitForEvent(c, l, "set", "")
	store.add(r)
	wait()
	return r
}

func dialRefused(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
	if err != nil {
		return true
	}
	conn.Close()
	return false
}

func (s *S) TestTCPResyncKeepsOneListenerPerRoute(c *C) {
	defer useReusePortListen()()
	store := newTestStore()
	l := &TCPListener{
		IP:        "127.0.0.1",
		syncer:    NewSyncer(store, "tcp"),
		discoverd: s.discoverd,
	}
	l.startPort, l.endPort = allocatePortRange(4)
	c.Assert(l.Start(), IsNil)
	defer l.Close()

	reservedPort := l.startPort
	onDemandPort := allocatePort()
	reservedAddr := fmt.Sprintf("127.0.0.1:%d", reservedPort)
	onDemandAddr := fmt.Sprintf("127.0.0.1:%d", onDemandPort)

	oldSrv := NewTCPTestServer("old")
	newSrv := NewTCPTestServer("new")
	defer oldSrv.Close()
	defer newSrv.Close()

	reserved := s.addTCPRouteToStore(c, l, store, router.TCPRoute{Service: "resync-old", Port: reservedPort}.ToRoute())
	onDemand := s.addTCPRouteToStore(c, l, store, router.TCPRoute{Service: "resync-old", Port: onDemandPort}.ToRoute())
	discoverdRegisterTCPService(c, l, "resync-old", oldSrv.Addr)
	assertTCPConn(c, reservedAddr, "old")
	assertTCPConn(c, onDemandAddr, "old")

	// a second route on the new service makes its discoverd cache exist so
	// the backend can be registered before the resync
	keep := s.addTCPRouteToStore(c, l, store, router.TCPRoute{Service: "resync-new", Port: l.startPort + 1}.ToRoute())
	discoverdRegisterTCPService(c, l, "resync-new", newSrv.Addr)

	presyncc := make(chan struct{})
	l.preSync = func() { <-presyncc }
	postsyncc := make(chan struct{})
	l.postSync = func(startc <-chan struct{}) {
		<-startc
		close(postsyncc)
	}

	// drop the event stream, change both routes while disconnected, resync
	store.closeStreams()
	for _, r := range []*router.Route{reserved, onDemand} {
		updated := *r
		updated.Service = "resync-new"
		store.routesMtx.Lock()
		store.routes[r.ID] = &updated
		store.routesMtx.Unlock()
	}
	close(presyncc)
	select {
	case <-postsyncc:
	case <-time.After(waitTimeout):
		c.Fatal("timeout waiting for tcp resync")
	}

	// an orphaned listener on the same port would keep taking a share of
	// connections for the old service
	for i := 0; i < 20; i++ {
		assertTCPConn(c, reservedAddr, "new")
		assertTCPConn(c, onDemandAddr, "new")
	}

	l.mtx.RLock()
	_, oldSvc := l.services["resync-old"]
	newRefs := 0
	if svc := l.services["resync-new"]; svc != nil {
		newRefs = svc.refs
	}
	routes := len(l.routes)
	l.mtx.RUnlock()
	c.Assert(oldSvc, Equals, false)
	c.Assert(newRefs, Equals, 3)
	c.Assert(routes, Equals, 3)

	// once the route is deleted nothing may accept on its port
	wait := waitForEvent(c, l, "remove", onDemand.ID)
	store.delete(onDemand)
	wait()
	c.Assert(dialRefused(onDemandAddr), Equals, true)

	// the reserved port goes back to the pool exactly once
	wait = waitForEvent(c, l, "remove", reserved.ID)
	store.delete(reserved)
	wait()
	l.mtx.RLock()
	_, pooled := l.listeners[reservedPort]
	pool := len(l.listeners)
	l.mtx.RUnlock()
	c.Assert(pooled, Equals, true)
	c.Assert(pool, Equals, 3)

	wait = waitForEvent(c, l, "remove", keep.ID)
	store.delete(keep)
	wait()
	l.mtx.RLock()
	_, svcLeft := l.services["resync-new"]
	l.mtx.RUnlock()
	c.Assert(svcLeft, Equals, false)
}

func (s *S) TestTCPRouteUpdateReplacesCertificate(c *C) {
	defer useReusePortListen()()
	store := newTestStore()
	l := &TCPListener{
		IP:        "127.0.0.1",
		syncer:    NewSyncer(store, "tcp"),
		discoverd: s.discoverd,
	}
	l.startPort, l.endPort = allocatePortRange(2)
	c.Assert(l.Start(), IsNil)
	defer l.Close()

	srv := NewTCPTestServer("tls-ok")
	defer srv.Close()

	for _, port := range []int{l.startPort, allocatePort()} {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		// generate fresh certs: the testutils cache is shared with other tests
		oldCert, err := tlscert.Generate([]string{"tcp-update.example.org"})
		c.Assert(err, IsNil)
		newCert, err := tlscert.Generate([]string{"tcp-update.example.org"})
		c.Assert(err, IsNil)
		route := router.TCPRoute{
			Service: "tls-update-tcp",
			Port:    port,
			TLSMode: router.TLSModeTerminate,
			Certificate: &router.Certificate{
				Cert: oldCert.Cert,
				Key:  oldCert.PrivateKey,
			},
		}.ToRoute()
		s.addTCPRouteToStore(c, l, store, route)
		unregister := discoverdRegisterTCPService(c, l, "tls-update-tcp", srv.Addr)

		c.Assert(peerCertFingerprint(c, addr), Equals, pemFingerprint(c, oldCert.Cert))

		updated := *route
		updated.Certificate = &router.Certificate{Cert: newCert.Cert, Key: newCert.PrivateKey}
		wait := waitForEvent(c, l, "set", route.ID)
		store.update(&updated)
		wait()

		for i := 0; i < 20; i++ {
			c.Assert(peerCertFingerprint(c, addr), Equals, pemFingerprint(c, newCert.Cert))
		}
		l.mtx.RLock()
		refs := l.services["tls-update-tcp"].refs
		l.mtx.RUnlock()
		c.Assert(refs, Equals, 1)

		unregister()
		wait = waitForEvent(c, l, "remove", route.ID)
		store.delete(&updated)
		wait()
	}
}

func peerCertFingerprint(c *C, addr string) string {
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	c.Assert(err, IsNil)
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	c.Assert(len(certs) > 0, Equals, true)
	return fingerprint(certs[0].Raw)
}

func pemFingerprint(c *C, certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	c.Assert(block, NotNil)
	return fingerprint(block.Bytes)
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:8])
}
