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

// controllerClient returns a controller API client using discoverd for DNS.
func controllerClient() (controller.Client, error) {
	instances, err := discoverd.NewService("controller").Instances()
	if err != nil {
		return nil, fmt.Errorf("discover controller instances: %w", err)
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no controller instances found")
	}
	discoverdDial := func(network, addr string) (net.Conn, error) {
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
	httpClient := &http.Client{Transport: &http.Transport{Dial: discoverdDial}}
	return controller.NewClientWithHTTP("http://controller.discoverd", instances[0].Meta["AUTH_KEY"], httpClient)
}
