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
	listeners     map[int]net.Listener

	mtx      sync.RWMutex
	services map[string]*service
	routes   map[string]*tcpRoute
	ports    map[int]*tcpRoute
	closed   bool

	// testing hooks
	preSync  func()
	postSync func(<-chan struct{})
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
	l.listeners = make(map[int]net.Listener)

	if l.startPort != 0 && l.endPort != 0 {
		for i := l.startPort; i <= l.endPort; i++ {
			addr := fmt.Sprintf("%s:%d", l.IP, i)
			listener, err := listenFunc("tcp4", addr)
			if err != nil {
				l.Close()
				return listenErr{addr, err}
			}
			l.listeners[i] = listener
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

		time.Sleep(2 * time.Second)

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
		s.Close()
	}
	for _, listener := range l.listeners {
		listener.Close()
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

	service, err := bindListenerService(&h.l.mtx, &h.l.closed, h.l.services, h.l.wm, h.l.discoverd, r.Service, r.DrainBackends)
	if err != nil {
		return err
	}
	if service == nil {
		return nil
	}

	h.l.mtx.Lock()
	defer h.l.mtx.Unlock()
	if h.l.closed {
		return nil
	}
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

	// A route that is already serving (an update, or every route again after
	// a resync) must hand its socket to the new route. Binding a fresh one
	// would succeed under SO_REUSEPORT and leave the old route accepting a
	// share of the connections with its old service and certificate.
	existing := h.l.routes[data.ID]
	samePort := existing != nil && existing.Port == r.Port
	fromPool, oldClosed := false, false
	if samePort {
		ln, err := existing.dupListener()
		if err != nil {
			// fall back to a fresh bind once the old socket is gone
			log.Printf("router: tcp route %s: reusing listener on port %d: %s", data.ID, r.Port, err)
			existing.l.Close()
			oldClosed = true
		} else {
			r.l = ln
		}
	} else if listener, ok := h.l.listeners[r.Port]; ok {
		r.l = listener
		fromPool = true
		delete(h.l.listeners, r.Port)
	}
	started := make(chan error)
	go r.Serve(started)
	if err := <-started; err != nil {
		if fromPool {
			h.l.listeners[r.Port] = r.l
		} else if samePort && r.l != nil {
			r.l.Close()
		}
		return err
	}
	service.refs++
	if existing != nil {
		if samePort {
			// the new route owns the socket; stop the old accept loop only
			if !oldClosed {
				existing.l.Close()
			}
		} else {
			existing.Close()
			if h.l.ports[existing.Port] == existing {
				delete(h.l.ports, existing.Port)
			}
		}
		h.l.releaseService(existing.service)
	}
	h.l.routes[data.ID] = r
	h.l.ports[r.Port] = r

	go h.l.wm.Send(&router.Event{Event: router.EventTypeRouteSet, ID: data.ID, Route: r.ToRoute()})
	return nil
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
	r.Close()
	h.l.releaseService(r.service)

	delete(h.l.routes, id)
	delete(h.l.ports, r.Port)
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

func (r *tcpRoute) Serve(started chan<- error) {
	var err error
	if r.l == nil {
		r.l, err = listenFunc("tcp4", r.addr)
	}
	if err == nil {
		r.rawL = r.l
		if r.tlsConfig != nil {
			r.l = tls.NewListener(r.l, r.tlsConfig)
		}
	}
	if err != nil {
		err = listenErr{r.addr, err}
	}
	started <- err
	if err != nil {
		return
	}
	for {
		conn, err := r.l.Accept()
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

func (r *tcpRoute) Close() {
	if r.l == nil {
		return
	}
	if r.Port >= r.parent.startPort && r.Port <= r.parent.endPort {
		ln, err := r.dupListener()
		if err != nil {
			log.Printf("router: returning tcp port %d to the pool: %s", r.Port, err)
		} else {
			r.parent.listeners[r.Port] = ln
		}
	}
	r.l.Close()
}

func (r *tcpRoute) ServeConn(conn net.Conn) {
	r.rp.ServeConn(context.Background(), connutil.CloseNotifyConn(conn))
}
