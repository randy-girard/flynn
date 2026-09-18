package cli

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	controller "github.com/flynn/flynn/controller/client"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/dialer"
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
	// Hijack (job attach, cluster backup) uses net.Dial, not Transport.Dial, so
	// the URL host must be dialable without *.discoverd in systemd-resolved.
	return controller.NewClientWithHTTP("http://"+instances[0].Addr, instances[0].Meta["AUTH_KEY"], httpClient)
}
