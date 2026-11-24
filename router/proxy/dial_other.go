//go:build !linux

package proxy

import (
	"net"
	"syscall"
	"time"
)

func setTCPUserTimeout(time.Duration) func(network, address string, c syscall.RawConn) error {
	return nil
}

func setConnUserTimeout(net.Conn, time.Duration) error {
	return nil
}
