package httphelper

import (
	"fmt"
	"net"
	"strings"
)

// ResolveDiscoverdAddr rewrites host:port when host ends in .discoverd.
// Flynn host daemons are not using overlay DNS (systemd-resolved does not
// serve *.discoverd), so HTTP clients must look the service up themselves.
func ResolveDiscoverdAddr(addr string, lookup func(service string) ([]string, error)) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(host, ".discoverd") {
		return addr, nil
	}
	if lookup == nil {
		return "", fmt.Errorf("lookup %s: no discoverd resolver", host)
	}
	service := strings.TrimSuffix(host, ".discoverd")
	addrs, err := lookup(service)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("lookup %s: no such host", host)
	}
	return addrs[0], nil
}
