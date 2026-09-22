package cli

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	controller "github.com/randy-girard/flynn/controller/client"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/dialer"
)

func discoverdHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Dial: discoverdDial}}
}

// lookupDiscoverdAddrs is replaced in tests. Production uses the local discoverd API.
var lookupDiscoverdAddrs = func(service string) ([]string, error) {
	return discoverd.NewService(service).Addrs()
}

// discoverdDialCursor rotates the first instance so a hung registration
// (still listed, TCP accept or blackhole) cannot pin every request.
var discoverdDialCursor uint64

// rotateDiscoverdAddrs returns addrs starting at a rotating index, then the rest.
// Callers try them in order so a refused first peer fails over in the same dial.
func rotateDiscoverdAddrs(addrs []string) []string {
	if len(addrs) == 0 {
		return nil
	}
	start := int(atomic.AddUint64(&discoverdDialCursor, 1)-1) % len(addrs)
	out := make([]string, len(addrs))
	for i := range addrs {
		out[i] = addrs[(start+i)%len(addrs)]
	}
	return out
}

func discoverdDial(network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(host, ".discoverd") {
		return dialer.Default.Dial(network, addr)
	}
	service := strings.TrimSuffix(host, ".discoverd")
	addrs, err := lookupDiscoverdAddrs(service)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("lookup %s: no such host", host)
	}
	var firstErr error
	for _, candidate := range rotateDiscoverdAddrs(addrs) {
		conn, err := dialer.Default.Dial(network, candidate)
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

// ClusterController is the discoverd-backed controller client (cluster admin).
func ClusterController() (controller.Client, error) {
	return controllerClient()
}

// controllerClient returns a controller API client using discoverd for DNS.
func controllerClient() (controller.Client, error) {
	instances, err := discoverd.NewService("controller").Instances()
	if err != nil {
		return nil, fmt.Errorf("discover controller instances: %w", err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no controller instances found")
	}
	httpClient := discoverdHTTPClient()
	// Use the discoverd DNS name, not a pinned instance IP. HTTP and Hijack
	// share Transport.Dial (discoverdDial), so systemd-resolved not knowing
	// *.discoverd is fine. Pinning would stick to a dead controller after a
	// deploy; the updater already uses this URL for ResumingStream.
	key := controllerAPIKey(instances[0].Meta)
	if key == "" {
		return nil, missingControllerKeyErr()
	}
	return controller.NewClientWithHTTP("http://controller.discoverd", key, httpClient)
}
