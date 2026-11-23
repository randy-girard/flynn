package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/flynn/go-check"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/router/proxy"
	router "github.com/randy-girard/flynn/router/types"
)

const httpCapBody = "App HTTP concurrency limit exceeded\n"

// lockedBuffer is the log capture for cap tests. Request goroutines write
// log15 lines while the test reads them.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// capHold blocks a backend until stop. Handlers also return when the request
// context ends so a failed assertion can close the server.
type capHold struct {
	ch   chan struct{}
	once sync.Once
}

func newCapHold() *capHold {
	return &capHold{ch: make(chan struct{})}
}

func (h *capHold) stop() {
	h.once.Do(func() { close(h.ch) })
}

func (h *capHold) wait(r *http.Request) {
	select {
	case <-h.ch:
	case <-r.Context().Done():
	}
}

func (s *S) TestHTTPAppConcurrencySharedAcrossRoutes(c *C) {
	const (
		parent = "controller/apps/cap-shared"
		other  = "controller/apps/cap-other"
		limit  = 2
	)
	hold := newCapHold()
	started := make(chan struct{}, limit)
	var webHits int64
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&webHits, 1)
		started <- struct{}{}
		hold.wait(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("web"))
	}))
	defer func() {
		hold.stop()
		web.Close()
	}()

	var adminHits int64
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&adminHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("admin"))
	}))
	defer admin.Close()

	var otherHits int64
	otherSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&otherHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("other"))
	}))
	defer otherSrv.Close()

	l := s.newHTTPListener(c)
	defer l.Close()

	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-web.example.com",
		Service:               "cap-shared-web",
		MaxConcurrentRequests: limit,
	}).ToRoute())
	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-admin.example.com",
		Service:               "cap-shared-admin",
		MaxConcurrentRequests: limit,
	}).ToRoute())
	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             other,
		Domain:                "cap-other.example.com",
		Service:               "cap-other-web",
		MaxConcurrentRequests: limit,
	}).ToRoute())

	defer discoverdRegisterHTTPService(c, l, "cap-shared-web", web.Listener.Addr().String())()
	defer discoverdRegisterHTTPService(c, l, "cap-shared-admin", admin.Listener.Addr().String())()
	defer discoverdRegisterHTTPService(c, l, "cap-other-web", otherSrv.Listener.Addr().String())()

	var buf lockedBuffer
	prev := logger.GetHandler()
	logger.SetHandler(log15.StreamHandler(&buf, log15.LogfmtFormat()))
	defer logger.SetHandler(prev)

	before := proxy.Snapshot()
	beforeWeb := metricRequests(before, "cap-shared-web")
	beforeAdmin := metricRequests(before, "cap-shared-admin")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := newReq("http://"+l.Addrs[0], "cap-web.example.com").WithContext(ctx)
			res, err := (&http.Client{}).Do(req)
			if err == nil {
				res.Body.Close()
			}
		}()
	}
	for i := 0; i < limit; i++ {
		select {
		case <-started:
		case <-time.After(waitTimeout):
			c.Fatal("timed out waiting for in-flight requests to reach the backend")
		}
	}

	status, body, hdr := httpCapGet(c, "http://"+l.Addrs[0], "cap-admin.example.com")
	c.Assert(status, Equals, http.StatusServiceUnavailable)
	c.Assert(body, Equals, httpCapBody)
	c.Assert(hdr.Get("Content-Type"), Equals, "text/plain; charset=utf-8")
	c.Assert(hdr.Get("Retry-After"), Equals, "1")
	c.Assert(atomic.LoadInt64(&adminHits), Equals, int64(0))
	c.Assert(atomic.LoadInt64(&webHits), Equals, int64(limit))

	status, body, _ = httpCapGet(c, "http://"+l.Addrs[0], "cap-other.example.com")
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(body, Equals, "other")
	c.Assert(atomic.LoadInt64(&otherHits), Equals, int64(1))

	after := proxy.Snapshot()
	c.Assert(metricRequests(after, "cap-shared-admin"), Equals, beforeAdmin)
	c.Assert(metricRequests(after, "cap-shared-web"), Equals, beforeWeb)
	c.Assert(buf.String(), Matches, "(?s).*http concurrency limit.*status=503.*parent_ref="+parent+".*")

	cancel()
	hold.stop()
	wg.Wait()
}

func (s *S) TestHTTPAppConcurrencyUsesMinimumAcrossRoutes(c *C) {
	const parent = "controller/apps/cap-min"
	hold := newCapHold()
	started := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		hold.wait(r)
		w.WriteHeader(http.StatusOK)
	}))
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fast"))
	}))
	defer func() {
		hold.stop()
		srv.Close()
		fast.Close()
	}()

	l := s.newHTTPListener(c)
	defer l.Close()

	// The higher limit is synced last. Last-write-wins would allow 8.
	low := s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-min-a.example.com",
		Service:               "cap-min-a",
		MaxConcurrentRequests: 2,
	}).ToRoute())
	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-min-b.example.com",
		Service:               "cap-min-b",
		MaxConcurrentRequests: 8,
	}).ToRoute())
	defer discoverdRegisterHTTPService(c, l, "cap-min-a", srv.Listener.Addr().String())()
	defer discoverdRegisterHTTPService(c, l, "cap-min-b", fast.Listener.Addr().String())()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := newReq("http://"+l.Addrs[0], "cap-min-a.example.com").WithContext(ctx)
			res, err := (&http.Client{}).Do(req)
			if err == nil {
				res.Body.Close()
			}
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(waitTimeout):
			c.Fatal("timed out waiting for in-flight requests")
		}
	}

	status, _, _ := httpCapGet(c, "http://"+l.Addrs[0], "cap-min-b.example.com")
	c.Assert(status, Equals, http.StatusServiceUnavailable)

	low.MaxConcurrentRequests = 8
	s.store.update(low)
	waitForRouteSet(c, l, low.ID, 8)

	status, body, _ := httpCapGet(c, "http://"+l.Addrs[0], "cap-min-b.example.com")
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(body, Equals, "fast")

	// Lowering takes effect from the first synced route, while the earlier
	// requests are still in flight.
	low.MaxConcurrentRequests = 1
	s.store.update(low)
	waitForRouteSet(c, l, low.ID, 1)
	status, _, _ = httpCapGet(c, "http://"+l.Addrs[0], "cap-min-b.example.com")
	c.Assert(status, Equals, http.StatusServiceUnavailable)

	cancel()
	hold.stop()
	wg.Wait()
}

func (s *S) TestHTTPAppConcurrencyUpgradeHoldsSlot(c *C) {
	const parent = "controller/apps/cap-upgrade"
	hold := newCapHold()
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.Header().Set("Connection", "Upgrade")
		w.Header().Set("Upgrade", "pinger")
		w.WriteHeader(http.StatusSwitchingProtocols)
		hj, ok := w.(http.Hijacker)
		c.Assert(ok, Equals, true)
		conn, _, err := hj.Hijack()
		c.Assert(err, IsNil)
		defer conn.Close()
		close(started)
		hold.wait(r)
	}))
	defer func() {
		hold.stop()
		srv.Close()
	}()

	l := s.newHTTPListener(c)
	defer l.Close()
	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-upgrade.example.com",
		Service:               "cap-upgrade",
		MaxConcurrentRequests: 1,
	}).ToRoute())
	defer discoverdRegisterHTTPService(c, l, "cap-upgrade", srv.Listener.Addr().String())()

	conn, err := net.Dial("tcp", l.Addrs[0])
	c.Assert(err, IsNil)
	defer conn.Close()
	_, err = conn.Write([]byte("GET / HTTP/1.1\r\nHost: cap-upgrade.example.com\r\nConnection: Upgrade\r\nUpgrade: pinger\r\n\r\n"))
	c.Assert(err, IsNil)
	c.Assert(strings.HasPrefix(readHTTPHeader(c, conn), "HTTP/1.1 101 "), Equals, true)
	select {
	case <-started:
	case <-time.After(waitTimeout):
		c.Fatal("timed out waiting for upgrade hijack")
	}

	status, body, _ := httpCapGet(c, "http://"+l.Addrs[0], "cap-upgrade.example.com")
	c.Assert(status, Equals, http.StatusServiceUnavailable)
	c.Assert(body, Equals, httpCapBody)

	hold.stop()
	conn.Close()
	deadline := time.Now().Add(waitTimeout)
	for {
		status, body, _ = httpCapGet(c, "http://"+l.Addrs[0], "cap-upgrade.example.com")
		if status == http.StatusOK && body == "ok" {
			return
		}
		if time.Now().After(deadline) {
			c.Fatalf("upgrade slot was not released, last status %d body %q", status, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *S) TestHTTPAppConcurrencyLongResponseHoldsSlot(c *C) {
	const parent = "controller/apps/cap-stream"
	hold := newCapHold()
	started := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hold" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("xy"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		once.Do(func() { close(started) })
		hold.wait(r)
		_, _ = w.Write([]byte("y"))
	}))
	defer func() {
		hold.stop()
		srv.Close()
	}()

	l := s.newHTTPListener(c)
	defer l.Close()
	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-stream.example.com",
		Service:               "cap-stream",
		MaxConcurrentRequests: 1,
	}).ToRoute())
	defer discoverdRegisterHTTPService(c, l, "cap-stream", srv.Listener.Addr().String())()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := newReq("http://"+l.Addrs[0]+"/hold", "cap-stream.example.com").WithContext(ctx)
	res, err := (&http.Client{}).Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	buf := make([]byte, 1)
	_, err = io.ReadFull(res.Body, buf)
	c.Assert(err, IsNil)
	c.Assert(string(buf), Equals, "x")
	select {
	case <-started:
	case <-time.After(waitTimeout):
		c.Fatal("timed out waiting for streamed response")
	}

	status, _, _ := httpCapGet(c, "http://"+l.Addrs[0]+"/free", "cap-stream.example.com")
	c.Assert(status, Equals, http.StatusServiceUnavailable)

	hold.stop()
	rest, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	c.Assert(string(rest), Equals, "y")

	deadline := time.Now().Add(waitTimeout)
	var body string
	for {
		status, body, _ = httpCapGet(c, "http://"+l.Addrs[0]+"/free", "cap-stream.example.com")
		if status == http.StatusOK && body == "xy" {
			return
		}
		if time.Now().After(deadline) {
			c.Fatalf("stream slot was not released, last status %d body %q", status, body)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *S) TestHTTPAppConcurrencyIdleKeepAliveDoesNotHoldSlot(c *C) {
	const parent = "controller/apps/cap-idle"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	l := s.newHTTPListener(c)
	defer l.Close()
	s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             parent,
		Domain:                "cap-idle.example.com",
		Service:               "cap-idle",
		MaxConcurrentRequests: 1,
	}).ToRoute())
	defer discoverdRegisterHTTPService(c, l, "cap-idle", srv.Listener.Addr().String())()

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: false}}
	defer client.CloseIdleConnections()
	for i := 0; i < 2; i++ {
		res, err := client.Do(newReq("http://"+l.Addrs[0], "cap-idle.example.com"))
		c.Assert(err, IsNil)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		c.Assert(err, IsNil)
		c.Assert(res.StatusCode, Equals, http.StatusOK)
		c.Assert(string(body), Equals, "ok")
	}
}

func (s *S) TestHTTPAppConcurrencyEmptyParentRefsDoNotShare(c *C) {
	hold := newCapHold()
	started := make(chan struct{})
	var once sync.Once
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		hold.wait(r)
		w.WriteHeader(http.StatusOK)
	}))
	free := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("free"))
	}))
	defer func() {
		hold.stop()
		blocked.Close()
		free.Close()
	}()

	l := s.newHTTPListener(c)
	defer l.Close()
	s.addRoute(c, l, (&router.HTTPRoute{
		Domain:                "cap-empty-a.example.com",
		Service:               "cap-empty-a",
		MaxConcurrentRequests: 1,
	}).ToRoute())
	s.addRoute(c, l, (&router.HTTPRoute{
		Domain:                "cap-empty-b.example.com",
		Service:               "cap-empty-b",
		MaxConcurrentRequests: 1,
	}).ToRoute())
	defer discoverdRegisterHTTPService(c, l, "cap-empty-a", blocked.Listener.Addr().String())()
	defer discoverdRegisterHTTPService(c, l, "cap-empty-b", free.Listener.Addr().String())()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		req := newReq("http://"+l.Addrs[0], "cap-empty-a.example.com").WithContext(ctx)
		res, err := (&http.Client{}).Do(req)
		if err == nil {
			res.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(waitTimeout):
		c.Fatal("timed out waiting for held request")
	}

	status, body, _ := httpCapGet(c, "http://"+l.Addrs[0], "cap-empty-b.example.com")
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(body, Equals, "free")
	cancel()
	hold.stop()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		c.Fatal("held request did not finish")
	}
}

func (s *S) TestHTTPAppConcurrencyOmittedLimitIsDefaultNotUnlimited(c *C) {
	l := s.newHTTPListener(c)
	defer l.Close()
	route := s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef: "controller/apps/cap-default",
		Domain:    "cap-default.example.com",
		Service:   "cap-default",
	}).ToRoute())
	c.Assert(route.MaxConcurrentRequests, Equals, 0)
	c.Assert(l.enforcedHTTPLimit(route.ParentRef), Equals, int64(router.DefaultMaxConcurrentRequests))

	zero := s.addRoute(c, l, (&router.HTTPRoute{
		ParentRef:             "controller/apps/cap-zero",
		Domain:                "cap-zero.example.com",
		Service:               "cap-zero",
		MaxConcurrentRequests: 0,
	}).ToRoute())
	c.Assert(l.enforcedHTTPLimit(zero.ParentRef), Equals, int64(router.DefaultMaxConcurrentRequests))
}

func httpCapGet(c *C, rawURL, host string) (int, string, http.Header) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	res, err := client.Do(newReq(rawURL, host))
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(body), res.Header
}

func metricRequests(rows []router.ServiceMetrics, service string) uint64 {
	for _, row := range rows {
		if row.Service == service {
			return row.Requests
		}
	}
	return 0
}

func readHTTPHeader(c *C, conn net.Conn) string {
	c.Assert(conn.SetReadDeadline(time.Now().Add(waitTimeout)), IsNil)
	br := bufio.NewReader(conn)
	var buf bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		c.Assert(err, IsNil)
		buf.WriteString(line)
		if line == "\r\n" {
			break
		}
	}
	return buf.String()
}

func waitForRouteSet(c *C, l *HTTPListener, id string, limit int) {
	deadline := time.Now().Add(waitTimeout)
	for {
		l.mtx.RLock()
		rt := l.routes[id]
		got := 0
		if rt != nil {
			got = rt.MaxConcurrentRequests
		}
		l.mtx.RUnlock()
		if rt != nil && got == limit && l.enforcedHTTPLimit(rt.ParentRef) == int64(limit) {
			return
		}
		if time.Now().After(deadline) {
			c.Fatalf("timed out waiting for route %s to enforce limit %d", id, limit)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func capKeys(h *httpConcurrency) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := make([]string, 0, len(h.apps))
	for k := range h.apps {
		keys = append(keys, k)
	}
	return keys
}

func (s *S) TestHTTPConcurrencyLastRouteRemovalDeletesCounter(c *C) {
	h := newHTTPConcurrency()
	h.setRoute("r1", "app", 10)
	h.setRoute("r2", "app", 5)
	h.setRoute("r1", "app", 10) // an update of the same route
	h.removeRoute("r1")
	c.Assert(capKeys(h), DeepEquals, []string{"app:app"})
	c.Assert(h.limitFor("app"), Equals, int64(5))
	h.removeRoute("r2")
	c.Assert(capKeys(h), HasLen, 0)

	// a route without a ParentRef has its own key
	h.setRoute("r3", "", 10)
	h.removeRoute("r3")
	c.Assert(capKeys(h), HasLen, 0)
}

func (s *S) TestHTTPConcurrencyParentChangeDeletesOldCounter(c *C) {
	h := newHTTPConcurrency()
	h.setRoute("r1", "a", 10)
	h.setRoute("r1", "b", 10)
	c.Assert(capKeys(h), DeepEquals, []string{"app:b"})
}

func (s *S) TestHTTPConcurrencyInFlightOutlivesRemovedRoute(c *C) {
	h := newHTTPConcurrency()
	h.setRoute("r1", "app", 1)
	_, _, ok := h.tryAcquire("r1", "app")
	c.Assert(ok, Equals, true)

	// the last route goes while a request is in flight: the slot is still
	// counted if the route comes back before that request ends
	h.removeRoute("r1")
	c.Assert(capKeys(h), DeepEquals, []string{"app:app"})
	h.setRoute("r1", "app", 1)
	_, _, ok = h.tryAcquire("r1", "app")
	c.Assert(ok, Equals, false)
	h.removeRoute("r1")

	h.release("r1", "app")
	c.Assert(capKeys(h), HasLen, 0)
}

func (s *S) TestHTTPConcurrencyStrayRequestDoesNotLeaveCounter(c *C) {
	h := newHTTPConcurrency()
	_, _, ok := h.tryAcquire("gone", "")
	c.Assert(ok, Equals, true)
	h.release("gone", "")
	c.Assert(capKeys(h), HasLen, 0)
}
