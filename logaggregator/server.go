package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/logaggregator/snapshot"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/keepalive"
	"github.com/randy-girard/flynn/pkg/syslog/rfc6587"
)

type Server struct {
	*Aggregator
	Cursors *HostCursors

	conf ServerConfig

	syslogListener net.Listener
	apiListener    net.Listener
	syslogWg       sync.WaitGroup
	syslogDone     chan struct{}

	hb discoverd.Heartbeater

	api      http.Handler
	shutdown chan struct{}

	snapshots *snapshotTracker
	// snapshotDrain bounds how long Shutdown lets snapshot responses that
	// are being written finish.
	snapshotDrain time.Duration
	// snapshotLinger is how long Shutdown keeps the process up after the
	// last snapshot was written, so its bytes still in flight arrive.
	snapshotLinger time.Duration

	testMessageHook chan struct{}
	// syslogSplit frames syslog connections; tests replace it.
	syslogSplit bufio.SplitFunc
}

type ServerConfig struct {
	SyslogAddr, ApiAddr string

	ServiceName string
	// APIKeys are the cluster keys the HTTP API accepts. Empty refuses every
	// route but the health check.
	APIKeys   []string
	Discoverd *discoverd.Client

	// BufferTTL is how long an app buffer with no new line and no follower
	// is kept (DefaultBufferTTL when zero).
	BufferTTL time.Duration
	// AppEvents, when set, is the controller event stream whose app
	// deletions drop that app's buffer.
	AppEvents appEventSource
}

func NewServer(conf ServerConfig) *Server {
	a := newAggregator(conf.BufferTTL)
	c := NewHostCursors()
	snaps := &snapshotTracker{}
	return &Server{
		Aggregator:     a,
		Cursors:        c,
		conf:           conf,
		api:            apiHandler(a, c, conf.APIKeys, snaps),
		snapshots:      snaps,
		snapshotDrain:  defaultSnapshotDrain,
		snapshotLinger: defaultSnapshotLinger,
		shutdown:       make(chan struct{}),
		syslogDone:     make(chan struct{}),
		syslogSplit:    rfc6587.Split,
	}
}

func (s *Server) Shutdown() {
	if s.hb != nil {
		// close discoverd service heartbeater
		if err := s.hb.Close(); err != nil {
			log15.Error("heartbeat shutdown error", "err", err)
		}
	}

	// shutdown listeners
	if s.syslogListener != nil {
		if err := s.syslogListener.Close(); err != nil {
			log15.Error("syslog listener shutdown error", "err", err)
		}
		<-s.syslogDone
	}
	if s.apiListener != nil {
		if err := s.apiListener.Close(); err != nil {
			log15.Error("api listener shutdown error", "err", err)
		}
	}
	// a deploy stops the old jobs as soon as the new ones are up, which is
	// while they read this instance's snapshot: let those reads finish
	if err := s.snapshots.drain(s.snapshotDrain, s.snapshotLinger); err != nil {
		log15.Error("stopping with snapshot reads unfinished", "waited", s.snapshotDrain, "err", err)
	}

	// close syslog client connections and stop the app deletion watcher
	// (not waited for: it may be inside a controller connect attempt)
	close(s.shutdown)
	s.syslogWg.Wait()

	// shutdown aggregator
	s.Aggregator.Shutdown()
}

func (s *Server) LoadSnapshotFile(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return s.LoadSnapshot(f)
}

// LoadSnapshot feeds the messages of a snapshot. The host cursors are only
// applied once it has been read to the end without error, so hosts resend
// what an incomplete one is missing.
func (s *Server) LoadSnapshot(r io.Reader) error {
	cursors := make(map[string]*utils.HostCursor)
	sc := snapshot.NewScanner(r)
	for sc.Scan() {
		cursor, err := utils.ParseHostCursor(sc.Message)
		if err != nil {
			return err
		}
		host := string(sc.Message.Hostname)
		if cur, ok := cursors[host]; !ok || cursor.After(*cur) {
			cursors[host] = cursor
		}
		s.Aggregator.Feed(sc.Message)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for host, c := range cursors {
		s.Cursors.Update(host, c)
	}
	return nil
}

func (s *Server) WriteSnapshotFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return snapshot.WriteTo(s.Aggregator.ReadAll(), f)
}

func (s *Server) SyslogAddr() net.Addr {
	return s.syslogListener.Addr()
}

func (s *Server) Start() error {
	var err error
	sl, err := net.Listen("tcp", s.conf.SyslogAddr)
	if err != nil {
		return err
	}
	s.syslogListener = keepalive.Listener(sl)

	al, err := net.Listen("tcp", s.conf.ApiAddr)
	if err != nil {
		return err
	}
	s.apiListener = keepalive.Listener(al)

	if s.conf.Discoverd != nil {
		s.hb, err = s.conf.Discoverd.AddServiceAndRegister(s.conf.ServiceName, s.conf.SyslogAddr)
		if err != nil {
			return err
		}
	}

	go s.runSyslog()
	go http.Serve(s.apiListener, s.api)

	if s.conf.AppEvents != nil {
		go watchAppDeletions(s.Aggregator, s.conf.AppEvents, s.shutdown, 10*time.Second)
	}

	return nil
}

func (s *Server) runSyslog() {
	defer close(s.syslogDone)
	for {
		conn, err := s.syslogListener.Accept()
		if err != nil {
			return
		}

		s.syslogWg.Add(1)
		go func(c net.Conn) {
			defer s.syslogWg.Done()
			s.drainSyslogConn(c)
		}(conn)
	}
}

func (s *Server) drainSyslogConn(conn net.Conn) {
	connDone := make(chan struct{})
	defer close(connDone)

	go func() {
		select {
		case <-connDone:
		case <-s.shutdown:
		}
		conn.Close()
	}()

	// A panic while reading one connection must only drop that connection,
	// not exit the process and stop log collection for the cluster.
	defer func() {
		if r := recover(); r != nil {
			log15.Error("syslog connection panic, closing it", "remote", conn.RemoteAddr(), "panic", r)
		}
	}()

	sc := bufio.NewScanner(conn)
	sc.Split(s.syslogSplit)
	for sc.Scan() {
		msgBytes := sc.Bytes()
		// slice in msgBytes could get modified on next Scan(), need to copy it
		msgCopy := make([]byte, len(msgBytes))
		copy(msgCopy, msgBytes)

		msg, cursor, err := utils.ParseMessage(msgCopy)
		if err != nil {
			log15.Error("rfc5424 parse error", "err", err)
		} else {
			s.Cursors.Update(string(msg.Hostname), cursor)
			s.Aggregator.Feed(msg)
		}
		if s.testMessageHook != nil {
			s.testMessageHook <- struct{}{}
		}
	}
	if err := sc.Err(); isSyslogFramingError(err) {
		log15.Error("syslog framing error, closing connection", "remote", conn.RemoteAddr(), "err", err)
	}
}

// isSyslogFramingError reports whether a scanner error came from a malformed
// frame rather than from the connection itself (reset, timeout, shutdown),
// which is normal when hosts reconnect and is not logged.
func isSyslogFramingError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	return !errors.As(err, &opErr) && !errors.Is(err, net.ErrClosed)
}

func NewHostCursors() *HostCursors {
	return &HostCursors{Data: make(map[string]*utils.HostCursor)}
}

// HostCursors are used to keep track of what messages a host has sent when
// resuming log streaming to a new server or after an interruption .
type HostCursors struct {
	mtx  sync.Mutex
	Data map[string]*utils.HostCursor
}

func (h *HostCursors) Update(id string, other *utils.HostCursor) {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	curr, ok := h.Data[id]
	if !ok || other.After(*curr) {
		h.Data[id] = other
	}
}

func (h *HostCursors) Get() map[string]*utils.HostCursor {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	res := make(map[string]*utils.HostCursor, len(h.Data))
	for k, v := range h.Data {
		res[k] = v
	}
	return res
}
