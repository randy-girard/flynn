package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/dialer"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

// updateDiscoverdDialBudget is how long one controller dial during
// `flynn-host update` keeps looking the service up again. The update replaces
// discoverd on every host and calls the controller right after; a replaced
// discoverd answered the lookup with 404 or with addresses from an old
// snapshot for about a second on the dev cluster (EDGE-69). The budget is
// generous so a slow catch-up or a controller restart does not fail the
// update; a caller's own context or HTTP timeout still ends it sooner.
var updateDiscoverdDialBudget = 2 * time.Minute

const (
	updateDiscoverdDialBackoff    = 250 * time.Millisecond
	updateDiscoverdDialMaxBackoff = 2 * time.Second
	updateDiscoverdDialLogEvery   = 5 * time.Second
)

// updateDiscoverdDialer resolves *.discoverd through the discoverd API like
// discoverdDial, but when the lookup fails or returns nothing, or every
// address it returns refuses, it looks the service up again with backoff
// until budget. Each attempt uses a fresh lookup, so a stale address is never
// dialled twice. A discoverd auth error is returned at once.
type updateDiscoverdDialer struct {
	budget  time.Duration
	backoff time.Duration
	log     log15.Logger
	// lookup defaults to lookupDiscoverdAddrs.
	lookup func(service string) ([]string, error)
}

func newUpdateDiscoverdDialer(log log15.Logger) *updateDiscoverdDialer {
	return &updateDiscoverdDialer{budget: updateDiscoverdDialBudget, backoff: updateDiscoverdDialBackoff, log: log}
}

func (d *updateDiscoverdDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(host, ".discoverd") {
		return dialer.Default.DialContext(ctx, network, addr)
	}
	service := strings.TrimSuffix(host, ".discoverd")
	lookup := d.lookup
	if lookup == nil {
		lookup = lookupDiscoverdAddrs
	}
	backoff := d.backoff
	if backoff <= 0 {
		backoff = updateDiscoverdDialBackoff
	}
	start := time.Now()
	deadline := start.Add(d.budget)
	var lastLog time.Time
	for attempt := 1; ; attempt++ {
		conn, err := d.dialOnce(ctx, network, host, service, lookup)
		if err == nil {
			if attempt > 1 && d.log != nil {
				d.log.Info(host+" reachable again", "attempts", attempt, "waited", time.Since(start).Round(time.Millisecond))
			}
			return conn, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("dial %s: %w (last error: %v)", host, ctxErr, err)
		}
		if discoverdAuthError(err) {
			return nil, err
		}
		if !time.Now().Add(backoff).Before(deadline) {
			return nil, fmt.Errorf("dial %s: no reachable %s instance after %s (%d lookups); last error: %w", host, service, d.budget, attempt, err)
		}
		if d.log != nil && time.Since(lastLog) >= updateDiscoverdDialLogEvery {
			d.log.Warn("waiting for "+host+" after a discoverd change, looking it up again",
				"attempt", attempt, "waited", time.Since(start).Round(time.Millisecond), "budget", d.budget, "err", err)
			lastLog = time.Now()
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("dial %s: %w (last error: %v)", host, ctx.Err(), err)
		case <-timer.C:
		}
		if backoff *= 2; backoff > updateDiscoverdDialMaxBackoff {
			backoff = updateDiscoverdDialMaxBackoff
		}
	}
}

func (d *updateDiscoverdDialer) dialOnce(ctx context.Context, network, host, service string, lookup func(string) ([]string, error)) (net.Conn, error) {
	addrs, err := lookup(service)
	if err != nil {
		return nil, fmt.Errorf("lookup %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("lookup %s: no such host", host)
	}
	var firstErr error
	for _, candidate := range rotateDiscoverdAddrs(addrs) {
		conn, err := dialer.Default.DialContext(ctx, network, candidate)
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, firstErr
}

// discoverdAuthError reports a discoverd 401/403: a wrong key does not heal.
func discoverdAuthError(err error) bool {
	var e hh.JSONError
	if errors.As(err, &e) {
		return e.Code == hh.UnauthorizedErrorCode || e.Code == hh.ForbiddenErrorCode
	}
	return false
}
