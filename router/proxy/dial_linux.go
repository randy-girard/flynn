package proxy

import (
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// setTCPUserTimeout fails a connection whose sent data stays unacknowledged
// for d, so requests in flight to a vanished backend error instead of hanging.
// Linux also counts the time the peer's receive window stays closed, even
// while the peer answers every zero-window probe, so d also limits how long a
// live backend may stop reading (EDGE-100).
func setTCPUserTimeout(d time.Duration) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(d.Milliseconds()))
		})
		if err != nil {
			return err
		}
		return sockErr
	}
}

// setConnUserTimeout changes the TCP_USER_TIMEOUT of an open connection. It
// does nothing for connections that are not TCP.
func setConnUserTimeout(c net.Conn, d time.Duration) error {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	return setTCPUserTimeout(d)("", "", raw)
}
