//go:build !linux

package proxy

import (
	"syscall"
	"time"
)

func setTCPUserTimeout(time.Duration) func(network, address string, c syscall.RawConn) error {
	return nil
}
