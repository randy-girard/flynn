package acme

import (
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/stream"
	router "github.com/randy-girard/flynn/router/types"
)

type stubStream struct{}

func (stubStream) Close() error { return nil }
func (stubStream) Err() error   { return nil }

type stubController struct {
	mu                 sync.Mutex
	streamCh           chan *ct.ManagedCertificate
	updates            []*ct.ManagedCertificate
	expiring           []*ct.ManagedCertificate
	failed             []*ct.ManagedCertificate
	listExpiringBefore time.Time
	listExpiringErr    error
	listFailedErr      error
	updateErr          error
}

func (c *stubController) StreamManagedCertificates(_ *time.Time, output chan *ct.ManagedCertificate) (stream.Stream, error) {
	c.mu.Lock()
	c.streamCh = output
	c.mu.Unlock()
	return stubStream{}, nil
}

func (c *stubController) UpdateManagedCertificate(cert *ct.ManagedCertificate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.updateErr != nil {
		return c.updateErr
	}
	cp := *cert
	if cert.LastError != nil {
		msg := *cert.LastError
		cp.LastError = &msg
	}
	c.updates = append(c.updates, &cp)
	return nil
}

func (c *stubController) ListExpiringManagedCertificates(before time.Time) ([]*ct.ManagedCertificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listExpiringBefore = before
	return c.expiring, c.listExpiringErr
}

func (c *stubController) ListFailedManagedCertificates() ([]*ct.ManagedCertificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failed, c.listFailedErr
}

func (c *stubController) CreateRoute(string, *router.Route) error { return nil }
func (c *stubController) DeleteRoute(string, string) error        { return nil }

func (c *stubController) waitStream(t *testing.T) chan *ct.ManagedCertificate {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		ch := c.streamCh
		c.mu.Unlock()
		if ch != nil {
			return ch
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("certificate stream not started")
	return nil
}

func (c *stubController) updateStatuses() []ct.ManagedCertificateStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ct.ManagedCertificateStatus, len(c.updates))
	for i, cert := range c.updates {
		out[i] = cert.Status
	}
	return out
}

func testService(ctrl *stubController) *Service {
	s := &Service{
		controller:      ctrl,
		handling:        make(map[string]struct{}),
		failCount:       make(map[string]int),
		now:             func() time.Time { return time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC) },
		renewalInterval: 24 * time.Hour,
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		log:             log15.New("test", "acme"),
	}
	s.handle = func(*ct.ManagedCertificate) {}
	return s
}

func TestRetryBackoffExponentialCap(t *testing.T) {
	if DefaultRenewalInterval != 24*time.Hour {
		t.Fatalf("DefaultRenewalInterval = %s, want 24h", DefaultRenewalInterval)
	}
	if RenewalWindow != 30*24*time.Hour {
		t.Fatalf("RenewalWindow = %s", RenewalWindow)
	}
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, time.Hour},
		{1, 2 * time.Hour},
		{2, 4 * time.Hour},
		{3, 8 * time.Hour},
		{4, 16 * time.Hour},
		{5, 24 * time.Hour},
		{10, 24 * time.Hour},
	}
	for _, c := range cases {
		if got := retryBackoff(c.failures); got != c.want {
			t.Errorf("retryBackoff(%d) = %s, want %s", c.failures, got, c.want)
		}
	}
}

func TestReconcileMarksExpiringPending(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	expires := now.Add(10 * 24 * time.Hour)
	ctrl := &stubController{
		expiring: []*ct.ManagedCertificate{{
			ID:        "c1",
			Domain:    "ex.com",
			Status:    ct.ManagedCertificateStatusIssued,
			ExpiresAt: &expires,
			Cert:      "keep-me",
			Key:       "keep-key",
		}},
	}
	s := testService(ctrl)
	s.now = func() time.Time { return now }

	s.reconcile()

	if !ctrl.listExpiringBefore.Equal(now.Add(RenewalWindow)) {
		t.Fatalf("ListExpiring before = %s, want %s", ctrl.listExpiringBefore, now.Add(RenewalWindow))
	}
	if len(ctrl.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(ctrl.updates))
	}
	got := ctrl.updates[0]
	if got.Status != ct.ManagedCertificateStatusPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
	if got.Cert != "keep-me" || got.Key != "keep-key" {
		t.Fatal("renewal must keep the existing cert/key so TLS keeps working")
	}
}

func TestReconcileRetriesFailedAfterBackoff(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	lastErr := now.Add(-30 * time.Minute)
	cert := &ct.ManagedCertificate{
		ID:          "c1",
		Domain:      "ex.com",
		Status:      ct.ManagedCertificateStatusFailed,
		LastErrorAt: &lastErr,
	}
	ctrl := &stubController{failed: []*ct.ManagedCertificate{cert}}
	s := testService(ctrl)
	s.now = func() time.Time { return now }

	s.reconcile()
	if len(ctrl.updates) != 0 {
		t.Fatalf("retried too early: %+v", ctrl.updates)
	}

	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	s.reconcile()
	if len(ctrl.updates) != 1 || ctrl.updates[0].Status != ct.ManagedCertificateStatusPending {
		t.Fatalf("updates = %+v", ctrl.updateStatuses())
	}
}

func TestReconcileFailedBackoffGrows(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	lastErr := now.Add(-90 * time.Minute)
	cert := &ct.ManagedCertificate{
		ID:          "c1",
		Domain:      "ex.com",
		Status:      ct.ManagedCertificateStatusFailed,
		LastErrorAt: &lastErr,
	}
	ctrl := &stubController{failed: []*ct.ManagedCertificate{cert}}
	s := testService(ctrl)
	s.now = func() time.Time { return now }
	s.failCount["c1"] = 2 // 4h backoff

	s.reconcile()
	if len(ctrl.updates) != 0 {
		t.Fatal("4h backoff should still be waiting at 90m")
	}

	s.now = func() time.Time { return now.Add(3 * time.Hour) }
	s.reconcile()
	if len(ctrl.updates) != 1 {
		t.Fatalf("expected retry after 4h backoff, updates=%d", len(ctrl.updates))
	}
}

func TestReconcileFailedWithoutTimestampRetries(t *testing.T) {
	ctrl := &stubController{failed: []*ct.ManagedCertificate{{
		ID:     "c1",
		Domain: "ex.com",
		Status: ct.ManagedCertificateStatusFailed,
	}}}
	s := testService(ctrl)
	s.reconcile()
	if len(ctrl.updates) != 1 {
		t.Fatal("failed certs with no timestamp should be retried")
	}
}

func TestReconcileSkipsBusyFailedDomain(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	lastErr := now.Add(-3 * time.Hour)
	ctrl := &stubController{failed: []*ct.ManagedCertificate{{
		ID:          "c1",
		Domain:      "ex.com",
		Status:      ct.ManagedCertificateStatusFailed,
		LastErrorAt: &lastErr,
	}}}
	s := testService(ctrl)
	s.handling["ex.com"] = struct{}{}
	s.reconcile()
	if len(ctrl.updates) != 0 {
		t.Fatal("must not re-queue a domain already being issued")
	}
}

func TestFailCertRecordsLastErrorAndBackoff(t *testing.T) {
	ctrl := &stubController{}
	s := testService(ctrl)
	cert := &ct.ManagedCertificate{ID: "c1", Domain: "ex.com", Status: ct.ManagedCertificateStatusPending}

	s.failCert(cert, "order_error", "rate limited")

	if cert.Status != ct.ManagedCertificateStatusFailed {
		t.Fatalf("status = %s", cert.Status)
	}
	if cert.LastError == nil || *cert.LastError != "order_error: rate limited" {
		t.Fatalf("LastError = %v", cert.LastError)
	}
	if cert.LastErrorAt == nil {
		t.Fatal("LastErrorAt not set")
	}
	if s.failCount["c1"] != 1 {
		t.Fatalf("failCount = %d", s.failCount["c1"])
	}
	if len(ctrl.updates) != 1 || ctrl.updates[0].Status != ct.ManagedCertificateStatusFailed {
		t.Fatalf("updates = %+v", ctrl.updateStatuses())
	}
}

func TestClearFailuresOnSuccess(t *testing.T) {
	s := testService(&stubController{})
	s.failCount["c1"] = 4
	s.clearFailures("c1")
	if _, ok := s.failCount["c1"]; ok {
		t.Fatal("failCount should be cleared after a successful issue")
	}
}

func TestRunDispatchesOnlyPending(t *testing.T) {
	ctrl := &stubController{}
	s := testService(ctrl)
	handled := make(chan string, 4)
	s.handle = func(cert *ct.ManagedCertificate) {
		handled <- cert.Domain
	}

	go s.Run()
	defer s.Stop()
	ch := ctrl.waitStream(t)

	ch <- &ct.ManagedCertificate{ID: "1", Domain: "issued.com", Status: ct.ManagedCertificateStatusIssued}
	ch <- &ct.ManagedCertificate{ID: "2", Domain: "failed.com", Status: ct.ManagedCertificateStatusFailed}
	ch <- &ct.ManagedCertificate{ID: "3", Domain: "pending.com", Status: ct.ManagedCertificateStatusPending}

	select {
	case d := <-handled:
		if d != "pending.com" {
			t.Fatalf("handled %s, want pending.com", d)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pending certificate")
	}
	select {
	case d := <-handled:
		t.Fatalf("handled extra domain %s", d)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRunReconcileEnqueuesExpiringForReissue(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	expires := now.Add(5 * 24 * time.Hour)
	expiring := &ct.ManagedCertificate{
		ID:        "c1",
		Domain:    "renew.example",
		Status:    ct.ManagedCertificateStatusIssued,
		ExpiresAt: &expires,
	}
	ctrl := &stubController{expiring: []*ct.ManagedCertificate{expiring}}
	s := testService(ctrl)
	s.now = func() time.Time { return now }
	s.renewalInterval = 24 * time.Hour
	handled := make(chan string, 1)
	s.handle = func(cert *ct.ManagedCertificate) {
		handled <- cert.Domain
	}

	go s.Run()
	defer s.Stop()
	ch := ctrl.waitStream(t)

	// Wait until reconcile marks the cert pending, then deliver that update
	// the way the controller stream would.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ctrl.mu.Lock()
		n := len(ctrl.updates)
		ctrl.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(ctrl.updateStatuses()) == 0 {
		t.Fatal("reconcile did not mark the expiring cert pending")
	}
	ch <- &ct.ManagedCertificate{
		ID:     "c1",
		Domain: "renew.example",
		Status: ct.ManagedCertificateStatusPending,
	}
	select {
	case d := <-handled:
		if d != "renew.example" {
			t.Fatalf("handled %s", d)
		}
	case <-time.After(time.Second):
		t.Fatal("pending renewal was not dispatched to handleCertificate")
	}
}

func TestRunReturnsWhenStreamCloses(t *testing.T) {
	ctrl := &stubController{}
	s := testService(ctrl)
	done := make(chan struct{})
	go func() {
		s.Run()
		close(done)
	}()
	ch := ctrl.waitStream(t)
	close(ch)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run hung after the certificate stream closed")
	}
}
