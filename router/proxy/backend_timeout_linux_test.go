//go:build linux

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/connutil"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/sys/unix"
)

// slowBackendPause is how long the slow backends below read nothing. It is
// longer than the 5 s dead-peer bound the router used to apply to every
// backend connection (EDGE-100).
const slowBackendPause = 10 * time.Second

// slowBodySize is larger than the loopback socket buffers on both hops, so the
// backend's receive window closes and the router's write blocks while the
// backend pauses.
const slowBodySize = 32 << 20

// listenSmallRcvBuf listens on loopback with a small receive buffer on the
// accepted connections, so a backend that stops reading closes its window
// quickly.
func listenSmallRcvBuf(t *testing.T) net.Listener {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, 64<<10)
		})
	}}
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func newTestProxy(addr string) *ReverseProxy {
	return NewReverseProxy(ReverseProxyConfig{
		BackendListFunc: func() []*router.Backend {
			return []*router.Backend{{Service: "test", Addr: addr}}
		},
		RequestTracker: nopTracker{},
		Logger:         log15.New(),
		Service:        "test",
	})
}

// EDGE-100: a backend that is alive but stops reading a request body for a
// while (tarreceive waiting for an upload slot, a blobstore PUT waiting on its
// store, git busy indexing) advertises a zero window and keeps answering the
// router's zero-window probes. The router cut such uploads after about 5 s
// with a plain 503, because Linux applies TCP_USER_TIMEOUT to a window that
// stays closed. The upload must complete.
func TestSlowReadingBackendIsNotCutOff(t *testing.T) {
	l := listenSmallRcvBuf(t)
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		n, _ := io.CopyN(ioutil.Discard, req.Body, 64<<10)
		time.Sleep(slowBackendPause)
		m, _ := io.Copy(ioutil.Discard, req.Body)
		body := strconv.FormatInt(n+m, 10)
		fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	}()

	srv := httptest.NewServer(newTestProxy(l.Addr().String()))
	defer srv.Close()

	start := time.Now()
	res, err := http.Post(srv.URL, "application/octet-stream", bytes.NewReader(make([]byte, slowBodySize)))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(data) != strconv.Itoa(slowBodySize) {
		t.Fatalf("after %v: status %d, body %.80q; want 200 and %d bytes read by the backend", time.Since(start).Round(100*time.Millisecond), res.StatusCode, data, slowBodySize)
	}
}

// The same for a TCP route: a backend that pauses reading must not have its
// connection cut while it is alive.
func TestSlowReadingTCPBackendIsNotCutOff(t *testing.T) {
	l := listenSmallRcvBuf(t)
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64<<10)
		n, _ := io.ReadFull(conn, buf)
		time.Sleep(slowBackendPause)
		m, _ := io.CopyN(ioutil.Discard, conn, int64(slowBodySize-n))
		fmt.Fprintf(conn, "%d\n", int64(n)+m)
	}()

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	p := newTestProxy(l.Addr().String())
	go func() {
		conn, err := front.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		p.ServeConn(context.Background(), connutil.CloseNotifyConn(conn))
	}()

	conn, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	go conn.Write(make([]byte, slowBodySize))
	conn.SetReadDeadline(time.Now().Add(slowBackendPause + 30*time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if line != strconv.Itoa(slowBodySize)+"\n" {
		t.Fatalf("after %v: backend answered %q (err %v), want %d bytes read", time.Since(start).Round(100*time.Millisecond), line, err, slowBodySize)
	}
}

// recordingDialer remembers the router side of every backend connection.
type recordingDialer struct {
	backendDialer
	mtx   sync.Mutex
	conns map[string]net.Conn // by local address
}

func (d *recordingDialer) Dial(network, addr string) (net.Conn, error) {
	c, err := d.backendDialer.Dial(network, addr)
	if err == nil {
		d.mtx.Lock()
		d.conns[c.LocalAddr().String()] = c
		d.mtx.Unlock()
	}
	return c, err
}

func (d *recordingDialer) conn(local string) net.Conn {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	return d.conns[local]
}

type deadPeerOpts struct {
	userTimeout time.Duration
	keepalive   time.Duration // idle + interval * count; 0 when off
}

func connDeadPeerOpts(t *testing.T, c net.Conn) deadPeerOpts {
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var o deadPeerOpts
	var serr error
	if err := raw.Control(func(fd uintptr) {
		get := func(level, opt int) int {
			v, err := unix.GetsockoptInt(int(fd), level, opt)
			if err != nil && serr == nil {
				serr = err
			}
			return v
		}
		o.userTimeout = time.Duration(get(unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT)) * time.Millisecond
		if get(unix.SOL_SOCKET, unix.SO_KEEPALIVE) != 0 {
			o.keepalive = time.Duration(get(unix.IPPROTO_TCP, unix.TCP_KEEPIDLE)+
				get(unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)*get(unix.IPPROTO_TCP, unix.TCP_KEEPCNT)) * time.Second
		}
	}); err != nil {
		t.Fatal(err)
	}
	if serr != nil {
		t.Fatal(serr)
	}
	return o
}

// A backend whose host vanishes must still be given up on in bounded time
// (a2b2f0aa0): within seconds for a request without a body, so a bodyless
// request on a pooled connection fails over to another backend, and within
// backendStreamDeadPeerTimeout while a request body or a TCP or upgraded
// stream is in flight, which is long enough for a backend that only pauses
// reading. Idle connections are covered by keepalive.
func TestBackendConnectionsDetectDeadPeerInBoundedTime(t *testing.T) {
	rec := &recordingDialer{backendDialer: dialer, conns: map[string]net.Conn{}}
	dialer = rec
	defer func() { dialer = rec.backendDialer }()

	seen := make(chan deadPeerOpts, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(ioutil.Discard, r.Body)
		if c := rec.conn(r.RemoteAddr); c != nil {
			seen <- connDeadPeerOpts(t, c)
		} else {
			seen <- deadPeerOpts{userTimeout: -1}
		}
	}))
	defer backend.Close()
	srv := httptest.NewServer(newTestProxy(backend.Listener.Addr().String()))
	defer srv.Close()

	// The same pooled connection carries every request in turn.
	for _, tc := range []struct {
		method string
		body   string
		min    time.Duration
		max    time.Duration
	}{
		{method: "GET", min: time.Second, max: 10 * time.Second},
		{method: "POST", body: "upload", min: 60 * time.Second, max: 5 * time.Minute},
		{method: "GET", min: time.Second, max: 10 * time.Second},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL, nil)
		if tc.body != "" {
			req, _ = http.NewRequest(tc.method, srv.URL, bytes.NewBufferString(tc.body))
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		o := <-seen
		if o.userTimeout < 0 {
			t.Fatalf("%s: backend connection was not dialed by the proxy dialer", tc.method)
		}
		if o.userTimeout < tc.min || o.userTimeout > tc.max {
			t.Errorf("%s (body %q): TCP_USER_TIMEOUT = %v, want between %v and %v", tc.method, tc.body, o.userTimeout, tc.min, tc.max)
		}
		if o.keepalive == 0 || o.keepalive > 30*time.Second {
			t.Errorf("%s: keepalive gives up on an idle dead backend after %v, want at most 30s", tc.method, o.keepalive)
		}
	}
}

// A backend that refuses an upload without reading it (tarreceive after its
// slot wait, a 413) answers while the router is still writing the body. The
// client must get that answer, not the router's own 503.
func TestBackendAnswerBeforeReadingBodyIsPassedOn(t *testing.T) {
	l := listenSmallRcvBuf(t)
	defer l.Close()
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			return
		}
		time.Sleep(time.Second)
		body := `{"code":"service_unavailable","message":"busy"}`
		fmt.Fprintf(conn, "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\nRetry-After: 30\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
		// stop reading, as a server that has answered and closes would
		time.Sleep(2 * time.Second)
	}()

	srv := httptest.NewServer(newTestProxy(l.Addr().String()))
	defer srv.Close()

	res, err := http.Post(srv.URL, "application/octet-stream", bytes.NewReader(make([]byte, slowBodySize)))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 503 || res.Header.Get("Retry-After") != "30" || !bytes.Contains(data, []byte(`"busy"`)) {
		t.Fatalf("status %d, Retry-After %q, body %.80q; want the backend's own 503", res.StatusCode, res.Header.Get("Retry-After"), data)
	}
}
