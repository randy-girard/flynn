package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/discoverd/cache"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/testutil"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/stream"
	router "github.com/randy-girard/flynn/router/types"
)

func init() {
	listenFunc = net.Listen
}

type discoverdClient interface {
	DiscoverdClient
	AddServiceAndRegister(string, string) (discoverd.Heartbeater, error)
}

// discoverdWrapper wraps a discoverd client to expose Close method that closes
// all heartbeaters
type discoverdWrapper struct {
	discoverdClient
	hbs []io.Closer
}

func (d *discoverdWrapper) AddServiceAndRegister(service, addr string) (discoverd.Heartbeater, error) {
	hb, err := d.discoverdClient.AddServiceAndRegister(service, addr)
	if err != nil {
		return nil, err
	}
	d.hbs = append(d.hbs, hb)
	return hb, nil
}

func (d *discoverdWrapper) Cleanup() {
	for _, hb := range d.hbs {
		hb.Close()
	}
	d.hbs = nil
}

type testStore struct {
	routesMtx sync.Mutex
	routes    map[string]*router.Route

	streamsMtx sync.Mutex
	streams    map[chan *router.Event]*stream.Basic

	// sortRouteList when set will be called to sort the list of routes
	// returned from List()
	sortRouteList func([]*router.Route)
}

func newTestStore() *testStore {
	return &testStore{
		routes:  make(map[string]*router.Route),
		streams: make(map[chan *router.Event]*stream.Basic),
	}
}

func (t *testStore) List() ([]*router.Route, error) {
	t.routesMtx.Lock()
	defer t.routesMtx.Unlock()
	routes := make([]*router.Route, 0, len(t.routes))
	for _, r := range t.routes {
		routes = append(routes, r)
	}
	if t.sortRouteList != nil {
		t.sortRouteList(routes)
	}
	return routes, nil
}

func (t *testStore) Watch(ch chan *router.Event) (stream.Stream, error) {
	s := stream.New()
	t.subscribe(ch, s)
	go func() {
		<-s.StopCh
		t.unsubscribe(ch)
	}()
	return s, nil
}

func (t *testStore) add(r *router.Route) {
	if r.ID == "" {
		r.ID = random.UUID()
	}
	if r.Path == "" {
		r.Path = "/"
	}
	t.routesMtx.Lock()
	t.routes[r.ID] = r
	t.routesMtx.Unlock()
	t.emit(&router.Event{
		Event: router.EventTypeRouteSet,
		ID:    r.ID,
		Route: r,
	})
}

func (t *testStore) update(r *router.Route) {
	t.routesMtx.Lock()
	t.routes[r.ID] = r
	t.routesMtx.Unlock()
	t.emit(&router.Event{
		Event: router.EventTypeRouteSet,
		ID:    r.ID,
		Route: r,
	})
}

func (t *testStore) delete(r *router.Route) {
	t.routesMtx.Lock()
	delete(t.routes, r.ID)
	t.routesMtx.Unlock()
	t.emit(&router.Event{
		Event: router.EventTypeRouteRemove,
		ID:    r.ID,
		Route: r,
	})
}

func (t *testStore) subscribe(ch chan *router.Event, stream *stream.Basic) {
	t.streamsMtx.Lock()
	defer t.streamsMtx.Unlock()
	t.streams[ch] = stream
}

func (t *testStore) unsubscribe(ch chan *router.Event) {
	t.streamsMtx.Lock()
	defer t.streamsMtx.Unlock()
	delete(t.streams, ch)
}

func (t *testStore) emit(event *router.Event) {
	t.streamsMtx.Lock()
	defer t.streamsMtx.Unlock()
	for ch, stream := range t.streams {
		select {
		case ch <- event:
		case <-stream.StopCh:
		}
	}
}

func (t *testStore) closeStreams() {
	t.streamsMtx.Lock()
	defer t.streamsMtx.Unlock()
	for ch, stream := range t.streams {
		stream.Error = errors.New("sync stopped")
		close(ch)
	}
	t.streams = make(map[chan *router.Event]*stream.Basic)
}

func (t *testStore) cleanup() {
	t.routesMtx.Lock()
	defer t.routesMtx.Unlock()
	t.routes = make(map[string]*router.Route)
}

func setup(t testutil.TestingT) (*discoverdWrapper, func()) {
	dc, killDiscoverd := testutil.BootDiscoverd(t, "")
	dw := &discoverdWrapper{discoverdClient: dc}

	return dw, func() {
		killDiscoverd()
	}
}

// Hook gocheck up to the "go test" runner
func Test(t *testing.T) { TestingT(t) }

type S struct {
	discoverd *discoverdWrapper
	store     *testStore
	cleanup   func()
}

var _ = Suite(&S{})

func (s *S) SetUpSuite(c *C) {
	s.discoverd, s.cleanup = setup(c)
	s.store = newTestStore()
}

func (s *S) TearDownSuite(c *C) {
	if s.cleanup != nil {
		s.cleanup()
	}
}

func (s *S) TearDownTest(c *C) {
	s.discoverd.Cleanup()
	s.store.cleanup()
}

// waitTimeout covers discoverd EventKindCurrent plus the async route "set"
// event. One second is too short under -race / smoke-test load.
const waitTimeout = 10 * time.Second

func waitForEvent(c *C, w Watcher, event router.EventType, id string) func() *router.Event {
	ch := make(chan *router.Event, 32)
	w.Watch(ch, false)
	return func() *router.Event {
		defer w.Unwatch(ch)
		for {
			timeout := time.After(waitTimeout)
			select {
			case e := <-ch:
				if e.Event == event && (id == "" || e.ID == id) {
					return e
				}
			case <-timeout:
				c.Fatalf("timeout exceeded waiting for %s %s", event, id)
				return nil
			}
		}
	}
}

func discoverdRegisterTCP(c *C, l *TCPListener, addr string) func() {
	return discoverdRegisterTCPService(c, l, "test", addr)
}

func discoverdRegisterTCPService(c *C, l *TCPListener, name, addr string) func() {
	dc := l.discoverd.(discoverdClient)
	sc := l.services[name].sc
	return discoverdRegister(c, dc, sc, name, addr)
}

func discoverdRegisterHTTP(c *C, l *HTTPListener, addr string) func() {
	return discoverdRegisterHTTPService(c, l, "test", addr)
}

func discoverdRegisterHTTPService(c *C, l *HTTPListener, name, addr string) func() {
	dc := l.discoverd.(discoverdClient)
	sc := l.services[name].sc
	return discoverdRegister(c, dc, sc, name, addr)
}

func discoverdSetLeaderHTTP(c *C, l *HTTPListener, name, id string) {
	dc := l.discoverd.(discoverdClient)
	sc := l.services[name].sc
	discoverdSetLeader(c, dc, sc, name, id)
}

func discoverdSetLeaderTCP(c *C, l *TCPListener, name, id string) {
	dc := l.discoverd.(discoverdClient)
	sc := l.services[name].sc
	discoverdSetLeader(c, dc, sc, name, id)
}

func discoverdSetLeader(c *C, dc discoverdClient, sc *cache.ServiceCache, name, id string) {
	done := make(chan struct{})
	go func() {
		events := make(chan *discoverd.Event)
		stream := sc.Watch(events, true)
		defer stream.Close()
		for event := range events {
			if event.Kind == discoverd.EventKindLeader && event.Instance.ID == id {
				close(done)
				return
			}
		}
	}()
	err := dc.Service(name).SetLeader(id)
	c.Assert(err, IsNil)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.Fatal("timed out waiting for discoverd leader change")
	}
}

func discoverdRegister(c *C, dc discoverdClient, sc *cache.ServiceCache, name, addr string) func() {
	done := make(chan struct{})
	go func() {
		events := make(chan *discoverd.Event)
		stream := sc.Watch(events, true)
		defer stream.Close()
		for event := range events {
			if event.Kind == discoverd.EventKindUp && event.Instance.Addr == addr {
				close(done)
				return
			}
		}
	}()
	hb, err := dc.AddServiceAndRegister(name, addr)
	c.Assert(err, IsNil)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.Fatal("timed out waiting for discoverd registration")
	}
	return discoverdUnregisterFunc(c, hb, sc)
}

func discoverdUnregisterFunc(c *C, hb discoverd.Heartbeater, sc *cache.ServiceCache) func() {
	return func() {
		done := make(chan struct{})
		started := make(chan struct{})
		go func() {
			events := make(chan *discoverd.Event)
			stream := sc.Watch(events, false)
			defer stream.Close()
			close(started)
			for event := range events {
				if event.Kind == discoverd.EventKindDown && event.Instance.Addr == hb.Addr() {
					close(done)
					return
				}
			}
		}()
		<-started
		c.Assert(hb.Close(), IsNil)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			c.Fatal("timed out waiting for discoverd unregister")
		}
	}
}

func (s *S) addRoute(c *C, l Listener, r *router.Route) *router.Route {
	return addRoute(c, l, s.store, r)
}

func addRoute(c *C, l Listener, store *testStore, r *router.Route) *router.Route {
	wait := waitForEvent(c, l, "set", "")
	store.add(r)
	wait()
	return r
}

func (s *S) removeRoute(c *C, l Listener, r *router.Route) {
	wait := waitForEvent(c, l, "remove", "")
	s.store.delete(r)
	wait()
}

var portAlloc uint32 = 4500

func allocatePort() int {
	return int(atomic.AddUint32(&portAlloc, 1))
}

func allocatePortRange(count int) (int, int) {
	max := int(atomic.AddUint32(&portAlloc, uint32(count)))
	return max - (count - 1), max
}

// stallingListener accepts TCP connections and never answers them, like a
// page host that has hung or a proxy that holds the connection open.
// If headers is set it sends response headers and part of a body first.
func stallingListener(t *testing.T, headers string) (string, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mtx sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			mtx.Lock()
			conns = append(conns, conn)
			mtx.Unlock()
			if headers != "" {
				conn.Write([]byte(headers))
			}
		}
	}()
	return "http://" + l.Addr().String() + "/503.html", func() {
		l.Close()
		mtx.Lock()
		defer mtx.Unlock()
		for _, conn := range conns {
			conn.Close()
		}
	}
}

func fetchError503PageWithin(t *testing.T, url string, timeout, bound time.Duration) []byte {
	t.Helper()
	prev := error503PageTimeout
	error503PageTimeout = timeout
	defer func() { error503PageTimeout = prev }()
	done := make(chan []byte, 1)
	start := time.Now()
	go func() { done <- fetchError503Page(url) }()
	select {
	case page := <-done:
		t.Logf("fetch returned after %s", time.Since(start))
		return page
	case <-time.After(bound):
		t.Fatalf("fetching ERROR_503_PAGE_URL still blocked after %s (timeout %s): the router would never open its listeners", bound, timeout)
		return nil
	}
}

func TestError503PageFetchTimesOut(t *testing.T) {
	url, cleanup := stallingListener(t, "")
	defer cleanup()
	if page := fetchError503PageWithin(t, url, 200*time.Millisecond, 5*time.Second); page != nil {
		t.Fatalf("expected no custom page (built-in fallback), got %q", page)
	}
}

func TestError503PageFetchTimesOutOnStalledBody(t *testing.T) {
	url, cleanup := stallingListener(t, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n<html>partial")
	defer cleanup()
	if page := fetchError503PageWithin(t, url, 200*time.Millisecond, 5*time.Second); page != nil {
		t.Fatalf("expected no custom page for a truncated body, got %q", page)
	}
}

func TestError503PageFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/503.html" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<html>down for maintenance</html>"))
	}))
	defer srv.Close()

	page := fetchError503PageWithin(t, srv.URL+"/503.html", time.Second, 5*time.Second)
	if string(page) != "<html>down for maintenance</html>" {
		t.Fatalf("unexpected page %q", page)
	}
	if page := fetchError503PageWithin(t, srv.URL+"/missing", time.Second, 5*time.Second); page != nil {
		t.Fatalf("expected no custom page for a 404, got %q", page)
	}
}
