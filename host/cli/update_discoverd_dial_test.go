package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

// EDGE-69: flynn-host update replaces discoverd on every host and then talks
// to the controller at once. A replaced discoverd answered the controller
// lookup with 404 "service not found" for about a second, and another one with
// controller addresses from an old snapshot that refused connections. The
// update failed on the first controller call after the swap; a rerun passed.
// The update's controller dial must look the controller up again until it is
// reachable, within a bounded budget, and then fail with a clear message.

type recordedLog struct {
	mu    sync.Mutex
	lines []string
}

func (r *recordedLog) logger() log15.Logger {
	l := log15.New()
	l.SetHandler(log15.FuncHandler(func(rec *log15.Record) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		line := rec.Msg
		for i := 0; i+1 < len(rec.Ctx); i += 2 {
			line += fmt.Sprintf(" %v=%v", rec.Ctx[i], rec.Ctx[i+1])
		}
		r.lines = append(r.lines, line)
		return nil
	}))
	return l
}

func (r *recordedLog) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.lines {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func acceptOnce(t *testing.T) (net.Listener, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close()
		close(accepted)
	}()
	return ln, accepted
}

func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

var errServiceNotFound = hh.JSONError{Code: hh.ObjectNotFoundErrorCode, Message: `service not found: "controller"`}

func TestUpdateDialWaitsForControllerServiceToReappear(t *testing.T) {
	ln, accepted := acceptOnce(t)
	missingUntil := time.Now().Add(300 * time.Millisecond)
	lookups := 0
	rec := &recordedLog{}
	d := &updateDiscoverdDialer{
		budget:  5 * time.Second,
		backoff: 20 * time.Millisecond,
		log:     rec.logger(),
		lookup: func(service string) ([]string, error) {
			lookups++
			if service != "controller" {
				t.Errorf("service=%s", service)
			}
			if time.Now().Before(missingUntil) {
				return nil, errServiceNotFound
			}
			return []string{ln.Addr().String()}, nil
		},
	}
	c, err := d.DialContext(context.Background(), "tcp", "controller.discoverd:80")
	if err != nil {
		t.Fatalf("dial after the service reappeared: %v", err)
	}
	c.Close()
	<-accepted
	if lookups < 2 {
		t.Fatalf("lookups=%d, want a retry", lookups)
	}
	if !rec.contains("waiting for controller.discoverd") || !rec.contains(`service not found: "controller"`) {
		t.Fatalf("retry not logged: %q", rec.lines)
	}
	if !rec.contains("controller.discoverd reachable again") {
		t.Fatalf("recovery not logged: %q", rec.lines)
	}
}

func TestUpdateDialReresolvesWhenStaleAddressRefuses(t *testing.T) {
	ln, accepted := acceptOnce(t)
	stale := refusedAddr(t)
	lookups := 0
	d := &updateDiscoverdDialer{
		budget:  5 * time.Second,
		backoff: 20 * time.Millisecond,
		log:     (&recordedLog{}).logger(),
		lookup: func(string) ([]string, error) {
			lookups++
			if lookups == 1 {
				// A just-replaced discoverd answering from an old snapshot.
				return []string{stale}, nil
			}
			return []string{ln.Addr().String()}, nil
		},
	}
	c, err := d.DialContext(context.Background(), "tcp", "controller.discoverd:80")
	if err != nil {
		t.Fatalf("dial after a stale address refused: %v", err)
	}
	c.Close()
	<-accepted
	if lookups != 2 {
		t.Fatalf("lookups=%d, want the controller looked up again", lookups)
	}
}

func TestUpdateDialFailsClearlyAfterBudget(t *testing.T) {
	for name, lookup := range map[string]func(string) ([]string, error){
		"missing": func(string) ([]string, error) { return nil, errServiceNotFound },
		"refused": func(string) ([]string, error) { return []string{refusedAddr(t)}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := &updateDiscoverdDialer{
				budget:  150 * time.Millisecond,
				backoff: 20 * time.Millisecond,
				log:     (&recordedLog{}).logger(),
				lookup:  lookup,
			}
			start := time.Now()
			_, err := d.DialContext(context.Background(), "tcp", "controller.discoverd:80")
			if err == nil {
				t.Fatal("dial succeeded with no reachable controller")
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("budget not honoured: %s", time.Since(start))
			}
			msg := err.Error()
			for _, want := range []string{"controller.discoverd", "150ms", "last error"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("error %q does not say %q", msg, want)
				}
			}
			if name == "missing" && !strings.Contains(msg, "service not found") {
				t.Fatalf("error %q lost the last lookup error", msg)
			}
			if name == "refused" && !strings.Contains(msg, "refused") {
				t.Fatalf("error %q lost the last dial error", msg)
			}
		})
	}
}

func TestUpdateDialDoesNotRetryUnauthorizedLookup(t *testing.T) {
	lookups := 0
	d := &updateDiscoverdDialer{
		budget:  5 * time.Second,
		backoff: 20 * time.Millisecond,
		log:     (&recordedLog{}).logger(),
		lookup: func(string) ([]string, error) {
			lookups++
			return nil, hh.JSONError{Code: hh.UnauthorizedErrorCode, Message: "valid Auth-Key header or Basic auth password required"}
		},
	}
	if _, err := d.DialContext(context.Background(), "tcp", "controller.discoverd:80"); err == nil {
		t.Fatal("dial succeeded")
	}
	if lookups != 1 {
		t.Fatalf("lookups=%d; a wrong discoverd key does not heal", lookups)
	}
}

func TestUpdateDialStopsWhenContextEnds(t *testing.T) {
	d := &updateDiscoverdDialer{
		budget:  time.Minute,
		backoff: 20 * time.Millisecond,
		log:     (&recordedLog{}).logger(),
		lookup:  func(string) ([]string, error) { return nil, errServiceNotFound },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := d.DialContext(ctx, "tcp", "controller.discoverd:80")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want the context error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("dial ignored the context: %s", time.Since(start))
	}
}

// The update's controller clients must use the retrying dial; the plain
// discoverdDial fails on the first empty lookup.
func TestUpdateControllerClientsUseRetryingDial(t *testing.T) {
	b, err := os.ReadFile("github_updater.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "newControllerHTTPClient(discoverdDial") {
		t.Fatal("update controller clients still use discoverdDial, which fails on the first empty lookup after a discoverd swap")
	}
	if strings.Count(src, "updateDial.DialContext") < 2 {
		t.Fatal("both update controller clients (deploy and repair) must dial through updateDiscoverdDialer")
	}
}
