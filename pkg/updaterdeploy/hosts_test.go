package updaterdeploy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/cluster"
)

type fakeHostLister struct {
	calls   int
	results []fakeHostListResult
}

type fakeHostListResult struct {
	hosts []*cluster.Host
	err   error
}

func (f *fakeHostLister) Hosts() ([]*cluster.Host, error) {
	i := f.calls
	f.calls++
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	r := f.results[i]
	return r.hosts, r.err
}

func withFastHostListRetry(t *testing.T, attempts int) {
	t.Helper()
	oldA, oldD := HostListAttempts, HostListDelay
	HostListAttempts, HostListDelay = attempts, time.Millisecond
	t.Cleanup(func() { HostListAttempts, HostListDelay = oldA, oldD })
}

func twoHosts() []*cluster.Host {
	return []*cluster.Host{
		cluster.NewHost("host1", "http://10.0.0.1:1113", nil, nil),
		cluster.NewHost("host2", "http://10.0.0.2:1113", nil, nil),
	}
}

func TestListHostsWithRetryRetriesTransientError(t *testing.T) {
	withFastHostListRetry(t, 3)
	l := &fakeHostLister{results: []fakeHostListResult{
		{err: errors.New("discoverd: connection refused")},
		{hosts: twoHosts()},
	}}
	hosts, err := ListHostsWithRetry(l, nil)
	if err != nil {
		t.Fatalf("want hosts after a transient error, got %v", err)
	}
	if len(hosts) != 2 || l.calls != 2 {
		t.Fatalf("want 2 hosts after 2 calls, got %d hosts after %d calls", len(hosts), l.calls)
	}
}

func TestListHostsWithRetryReturnsErrorAfterAttempts(t *testing.T) {
	withFastHostListRetry(t, 3)
	l := &fakeHostLister{results: []fakeHostListResult{{err: errors.New("discoverd: connection refused")}}}
	hosts, err := ListHostsWithRetry(l, nil)
	if err == nil {
		t.Fatalf("want an error when every host list fails, got %d hosts", len(hosts))
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("want the last host list error wrapped, got %v", err)
	}
	if l.calls != 3 {
		t.Fatalf("want 3 attempts, got %d", l.calls)
	}
}

// An empty host list from discoverd is a lookup that has not settled, not a
// zero-node cluster: the updater itself runs on a host. Treating it as
// success would skip host-infra replacement and pick single-node strategies.
func TestListHostsWithRetryTreatsEmptyListAsError(t *testing.T) {
	withFastHostListRetry(t, 2)
	l := &fakeHostLister{results: []fakeHostListResult{{hosts: nil}}}
	if _, err := ListHostsWithRetry(l, nil); err == nil {
		t.Fatal("want an error for an empty host list")
	}
	if l.calls != 2 {
		t.Fatalf("want 2 attempts, got %d", l.calls)
	}
}

func TestListHostsWithRetryNilLister(t *testing.T) {
	if _, err := ListHostsWithRetry(nil, nil); err == nil {
		t.Fatal("want an error for a nil host lister")
	}
}
