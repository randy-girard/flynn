package cli

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	controller "github.com/randy-girard/flynn/controller/client"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/dialer"
)

func discoverdHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Dial: discoverdDial}}
}

func discoverdDial(network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(host, ".discoverd") {
		service := strings.TrimSuffix(host, ".discoverd")
		addrs, err := discoverd.NewService(service).Addrs()
		if err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("lookup %s: no such host", host)
		}
		addr = addrs[0]
	}
	return dialer.Default.Dial(network, addr)
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
	return controller.NewClientWithHTTP("http://controller.discoverd", instances[0].Meta["AUTH_KEY"], httpClient)
}
