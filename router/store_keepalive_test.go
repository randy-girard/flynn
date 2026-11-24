//go:build linux

package main

import (
	"net"
	"net/http"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// EDGE-94: when the node of the controller a router streams from reboots,
// the connection dies without a FIN or RST. The router only learns of it
// from TCP keepalive, which took about 2.5 minutes with the default dialer
// (30 s idle, 30 s interval): the route list was out of date for that long.
// The controller client's connections must give up on a dead peer within
// 30 seconds.
func TestControllerClientDetectsDeadPeerWithin30s(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	tr, ok := controllerHTTPClient().Transport.(*http.Transport)
	if !ok || tr.Dial == nil {
		t.Fatal("controller client transport has no dialer")
	}
	conn, err := tr.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, err := conn.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var on, idle, intvl, cnt int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		get := func(level, opt int) int {
			v, err := unix.GetsockoptInt(int(fd), level, opt)
			if err != nil && serr == nil {
				serr = err
			}
			return v
		}
		on = get(unix.SOL_SOCKET, unix.SO_KEEPALIVE)
		idle = get(unix.IPPROTO_TCP, unix.TCP_KEEPIDLE)
		intvl = get(unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)
		cnt = get(unix.IPPROTO_TCP, unix.TCP_KEEPCNT)
	}); err != nil {
		t.Fatal(err)
	}
	if serr != nil {
		t.Fatal(serr)
	}
	if on == 0 {
		t.Fatal("keepalive is off on controller connections")
	}
	if d := time.Duration(idle+intvl*cnt) * time.Second; d > 30*time.Second {
		t.Fatalf("a dead controller is noticed after %v (idle %ds, interval %ds, %d probes), want at most 30s", d, idle, intvl, cnt)
	}
}
