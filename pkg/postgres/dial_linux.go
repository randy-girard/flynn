package postgres

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// setTCPUserTimeout fails a connection whose sent data stays unacknowledged
// for d, so queries in flight to a vanished primary error instead of hanging.
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
