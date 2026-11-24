package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/randy-girard/flynn/pkg/connutil"
	"github.com/randy-girard/flynn/pkg/keepalive"
	"github.com/randy-girard/flynn/pkg/tlsconfig"
	"github.com/randy-girard/flynn/router/proxy"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

type TCPListener struct {
	Watcher

	IP string

	discoverd DiscoverdClient
	syncer    *Syncer
	wm        *WatchManager
	stopSync  func()

	startPort     int
	endPort       int
	reservedPorts []int

	// pool holds the pre-bound ports (startPort to endPort) that no route
	// owns. The sockets are bound but not listening, so the port stays
	// reserved and the kernel refuses connections to it. A route on a pool
	// port listens on that socket; when the route goes, a fresh bound socket
	// takes the port back. Each port is either in the pool or owned by
	// exactly one route in ports, never both.
	pool map[int]*keepalive.BoundSocket

	mtx      sync.RWMutex
	services map[string]*service
	routes   map[string]*tcpRoute
	ports    map[int]*tcpRoute
	closed   bool

	// testing hooks
	preSync    func()
	postSync   func(<-chan struct{})
	retryDelay time.Duration // wait before a resync; 0 means 2s
}

func (l *TCPListener) Start() error {
	ctx := context.Background() // TODO(benburkert): make this an argument
	ctx, l.stopSync = context.WithCancel(ctx)

	if l.Watcher != nil {
		return errors.New("router: tcp listener already started")
	}
	if l.wm == nil {
		l.wm = NewWatchManager()
	}
	l.Watcher = l.wm

	if l.syncer == nil {
		return errors.New("router: tcp listener missing syncer")
	}

	l.services = make(map[string]*service)
	l.routes = make(map[string]*tcpRoute)
	l.ports = make(map[int]*tcpRoute)
	l.pool = make(map[int]*keepalive.BoundSocket)

	if l.startPort != 0 && l.endPort != 0 {
		for i := l.startPort; i <= l.endPort; i++ {
			addr := fmt.Sprintf("%s:%d", l.IP, i)
			sock, err := bindFunc("tcp4", addr)
			if err != nil {
				l.Close()
				return listenErr{addr, err}
			}
			l.pool[i] = sock
		}
	}

	// TODO(benburkert): the sync API cannot handle routes deleted while the
	// listen/notify connection is disconnected
	if err := l.startSync(ctx); err != nil {
		l.Close()
		return err
	}

	return nil
}

func (l *TCPListener) startSync(ctx context.Context) error {
	errc := make(chan error)
	startc := l.doSync(ctx, errc)

	select {
	case err := <-errc:
		return err
	case <-startc:
		go l.runSync(ctx, errc)
		return nil
	}
}

func (l *TCPListener) runSync(ctx context.Context, errc chan error) {
	err := <-errc

	for {
		if err == nil {
			return
		}
		log.Printf("router: tcp sync error: %s", err)

		delay := l.retryDelay
		if delay == 0 {
			delay = 2 * time.Second
		}
		time.Sleep(delay)

		if l.preSync != nil {
			l.preSync()
		}

		startc := l.doSync(ctx, errc)

		if l.postSync != nil {
			l.postSync(startc)
		}

		err = <-errc
	}
}

func (l *TCPListener) doSync(ctx context.Context, errc chan<- error) <-chan struct{} {
	startc := make(chan struct{})

	go func() { errc <- l.syncer.Sync(ctx, &tcpSyncHandler{l: l}, startc) }()

	return startc
}

func (l *TCPListener) Close() error {
	l.mtx.Lock()
	defer l.mtx.Unlock()
	if l.closed {
		return nil
	}
	l.stopSync()
	for _, s := range l.routes {
		s.closeListener(false)
	}
	for _, sock := range l.pool {
		sock.Close()
	}
	l.closed = true
	return nil
}

type tcpSyncHandler struct {
	l *TCPListener
}

func (h *tcpSyncHandler) Current() map[string]struct{} {
	h.l.mtx.RLock()
	defer h.l.mtx.RUnlock()
	ids := make(map[string]struct{}, len(h.l.routes))
	for id := range h.l.routes {
		ids[id] = struct{}{}
	}
	return ids
}

func (h *tcpSyncHandler) Set(data *router.Route) error {
	route := data.TCPRoute()
	r := &tcpRoute{
		TCPRoute: route,
		addr:     h.l.IP + ":" + strconv.Itoa(route.Port),
		parent:   h.l,
	}
	if cert := r.Certificate; cert != nil && cert.Cert != "" && cert.Key != "" && router.TerminatesTLS(r.TLSMode) {
		kp, err := tls.X509KeyPair([]byte(cert.Cert), []byte(cert.Key))
		if err != nil {
			return err
		}
		r.tlsConfig = tlsconfig.SecureCiphers(&tls.Config{
			Certificates: []tls.Certificate{kp},
			MinVersion:   tls.VersionTLS12,
		})
		r.Certificate = nil
	}

	var service *service
	for {
		// cache.New waits for discoverd EventKindCurrent; do that without
		// the listener mutex
		svc, err := bindListenerService(&h.l.mtx, &h.l.closed, h.l.services, h.l.wm, h.l.discoverd, r.Service, r.DrainBackends)
		if err != nil {
			return err
		}
		if svc == nil {
			return nil
		}
		h.l.mtx.Lock()
		if h.l.closed {
			h.l.mtx.Unlock()
			return nil
		}
		if !svc.closed {
			service = svc
			break
		}
		// the last route using it went while the lock was not held
		h.l.mtx.Unlock()
	}
	defer h.l.mtx.Unlock()
	service.refs++
	r.service = service
	var bf proxy.BackendListFunc
	if r.Leader {
		bf = backendFunc(r.Service, service.sc.Leader)
	} else {
		bf = backendFunc(r.Service, service.sc.Instances)
	}
	r.rp = proxy.NewReverseProxy(proxy.ReverseProxyConfig{
		BackendListFunc: bf,
		RequestTracker:  service,
		Logger:          logger,
	})

	// The route that serves this port now hands its socket to the new
	// route: the same id (an update, or every route again after a resync),
	// or another id (the route was deleted and re-created on the port while
	// the event stream was down, or the new id's set arrived before the old
	// id's remove). Binding a fresh socket would succeed under SO_REUSEPORT
	// and leave the old socket taking a share of the connections.
	existing := h.l.routes[data.ID]
	holder := h.l.ports[r.Port]
	ln, err := h.l.takePortLocked(r, holder)
	if holder != nil && holder != existing {
		// stopped accepting in takePortLocked, success or not
		h.l.dropRouteLocked(holder)
	}
	if err != nil {
		if existing != nil && existing == holder {
			h.l.dropRouteLocked(existing)
		}
		h.l.releaseService(service)
		return err
	}
	r.rawL = ln
	r.l = ln
	if r.tlsConfig != nil {
		r.l = tls.NewListener(ln, r.tlsConfig)
	}
	if existing != nil {
		if existing != holder {
			// the route moved to another port
			existing.closeListener(true)
			if h.l.ports[existing.Port] == existing {
				delete(h.l.ports, existing.Port)
			}
		}
		h.l.releaseService(existing.service)
	}
	h.l.routes[data.ID] = r
	h.l.ports[r.Port] = r
	go r.Serve(r.l)

	go h.l.wm.Send(&router.Event{Event: router.EventTypeRouteSet, ID: data.ID, Route: r.ToRoute()})
	return nil
}

// takePortLocked returns a listener on r's port: a dup of the socket of the
// route that holds the port (which stops accepting), the port's pre-bound
// socket, or a fresh one. The caller holds l.mtx.
func (l *TCPListener) takePortLocked(r *tcpRoute, holder *tcpRoute) (net.Listener, error) {
	if holder != nil {
		ln, err := holder.dupListener()
		// the holder stops accepting; with a dup the socket and its queued
		// connections stay with r
		holder.l.Close()
		if err == nil {
			return ln, nil
		}
		log.Printf("router: tcp route %s: reusing listener on port %d: %s", r.ID, r.Port, err)
	}
	if sock, ok := l.pool[r.Port]; ok {
		delete(l.pool, r.Port)
		ln, err := sock.Listen()
		if err != nil {
			return nil, listenErr{r.addr, err}
		}
		return ln, nil
	}
	ln, err := listenFunc("tcp4", r.addr)
	if err != nil {
		return nil, listenErr{r.addr, err}
	}
	return ln, nil
}

// dropRouteLocked forgets a route whose socket another route took over (or
// that lost it). Its own remove, if one follows, is then a no-op. The caller
// holds l.mtx.
func (l *TCPListener) dropRouteLocked(r *tcpRoute) {
	if l.routes[r.ID] == r {
		delete(l.routes, r.ID)
	}
	if l.ports[r.Port] == r {
		delete(l.ports, r.Port)
	}
	l.releaseService(r.service)
	go l.wm.Send(&router.Event{Event: router.EventTypeRouteRemove, ID: r.ID, Route: r.ToRoute()})
}

func (h *tcpSyncHandler) Remove(id string) error {
	h.l.mtx.Lock()
	defer h.l.mtx.Unlock()
	if h.l.closed {
		return nil
	}
	r, ok := h.l.routes[id]
	if !ok {
		return ErrNotFound
	}
	r.closeListener(true)
	h.l.releaseService(r.service)

	delete(h.l.routes, id)
	if h.l.ports[r.Port] == r {
		delete(h.l.ports, r.Port)
	}
	go h.l.wm.Send(&router.Event{Event: router.EventTypeRouteRemove, ID: id, Route: r.ToRoute()})
	return nil
}

type tcpRoute struct {
	parent *TCPListener
	*router.TCPRoute
	rawL      net.Listener
	l         net.Listener
	addr      string
	service   *service
	rp        *proxy.ReverseProxy
	tlsConfig *tls.Config
}

func (r *tcpRoute) Serve(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			break
		}
		go r.ServeConn(conn)
	}
}

// releaseService drops one route's reference to svc and closes it when no
// route uses it any more. The caller holds l.mtx.
func (l *TCPListener) releaseService(svc *service) {
	if svc == nil {
		return
	}
	svc.refs--
	if svc.refs <= 0 {
		svc.Close()
		if l.services[svc.name] == svc {
			delete(l.services, svc.name)
		}
	}
}

// dupListener returns a second listener on the route's socket, so the socket
// stays bound (with its accept queue) after r.l is closed.
func (r *tcpRoute) dupListener() (net.Listener, error) {
	raw := r.rawL
	if raw == nil {
		raw = r.l
	}
	tcpLn, ok := raw.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("listener %T is not a TCP listener", raw)
	}
	fd, err := tcpLn.File()
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	return net.FileListener(fd)
}

// closeListener stops the route's listener. With toPool, a port in the
// pre-bound range goes back to the pool as a bound socket that does not
// listen, so connections to it are refused rather than queued for nobody.
// The new socket is bound before the old one closes, so the port is held
// throughout. The caller holds the parent's mtx.
func (r *tcpRoute) closeListener(toPool bool) {
	if r.l == nil {
		return
	}
	p := r.parent
	if !toPool || r.Port < p.startPort || r.Port > p.endPort || p.startPort == 0 {
		r.l.Close()
		return
	}
	sock, err := bindFunc("tcp4", r.addr)
	r.l.Close()
	if err != nil {
		// the listening socket held the port; try again now that it is gone
		sock, err = bindFunc("tcp4", r.addr)
	}
	if err != nil {
		log.Printf("router: returning tcp port %d to the pool: %s", r.Port, err)
		return
	}
	if old := p.pool[r.Port]; old != nil {
		old.Close()
	}
	p.pool[r.Port] = sock
}

func (r *tcpRoute) ServeConn(conn net.Conn) {
	r.rp.ServeConn(context.Background(), connutil.CloseNotifyConn(conn))
}
