package updaterdeploy

import (
	"errors"
	"fmt"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// HostLister lists the flynn-host peers registered in discoverd.
// *cluster.Client satisfies it.
type HostLister interface {
	Hosts() ([]*cluster.Host, error)
}

// Host list retry budget. Exported so the updater tests do not sleep.
var (
	HostListAttempts = 6
	HostListDelay    = 5 * time.Second
)

var errNoHosts = errors.New("discoverd returned no hosts")

// ListHostsWithRetry lists the cluster hosts, retrying transient discoverd
// errors. An empty list counts as a failure: the updater runs on a host, so
// zero hosts means discoverd has not settled. Callers must not fall back to a
// guess (skip the host-infra swap, assume one node) when this errors.
func ListHostsWithRetry(l HostLister, log log15.Logger) ([]*cluster.Host, error) {
	if l == nil {
		return nil, fmt.Errorf("list cluster hosts: nil host lister")
	}
	attempts := HostListAttempts
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		var hosts []*cluster.Host
		hosts, err = l.Hosts()
		if err == nil && len(hosts) > 0 {
			return hosts, nil
		}
		if err == nil {
			err = errNoHosts
		}
		if attempt == attempts {
			break
		}
		if log != nil {
			log.Warn("error listing cluster hosts, retrying", "attempt", attempt, "err", err)
		}
		if HostListDelay > 0 {
			time.Sleep(HostListDelay)
		}
	}
	return nil, fmt.Errorf("list cluster hosts after %d attempts: %w", attempts, err)
}
