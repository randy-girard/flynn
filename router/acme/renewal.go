package acme

import (
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	// DefaultRenewalInterval is how often the ACME service looks for expiring
	// and failed certificates. Daily is enough for a 30-day renewal window
	// and stays inside Let's Encrypt rate limits.
	DefaultRenewalInterval = 24 * time.Hour

	// RenewalWindow is how far ahead of expiry issued certificates are
	// reset to pending so handleCertificate can reissue them.
	RenewalWindow = 30 * 24 * time.Hour

	initialRetryBackoff = 2 * time.Minute
	maxRetryBackoff     = 24 * time.Hour

	// pendingRetryAge is how long a certificate may sit in pending before
	// reconcile re-dispatches it. Fresh stream events are handled immediately;
	// this covers missed events after plugin install.
	pendingRetryAge = 2 * time.Minute
)

func retryBackoff(failures int) time.Duration {
	if failures < 0 {
		failures = 0
	}
	d := initialRetryBackoff
	for i := 0; i < failures; i++ {
		if d >= maxRetryBackoff/2 {
			return maxRetryBackoff
		}
		d *= 2
	}
	if d > maxRetryBackoff {
		return maxRetryBackoff
	}
	return d
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) renewalLoop(stop <-chan struct{}) {
	interval := s.renewalInterval
	if interval <= 0 {
		interval = DefaultRenewalInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.reconcile()
	for {
		select {
		case <-ticker.C:
			s.reconcile()
		case <-stop:
			return
		case <-s.stop:
			return
		}
	}
}

func (s *Service) reconcile() {
	s.renewExpiring()
	s.retryFailed()
	s.retryPending()
}

func (s *Service) renewExpiring() {
	before := s.currentTime().Add(RenewalWindow)
	certs, err := s.controller.ListExpiringManagedCertificates(before)
	if err != nil {
		s.log.Error("error listing expiring certificates", "err", err)
		return
	}
	for _, cert := range certs {
		if cert == nil {
			continue
		}
		s.log.Info("renewing expiring certificate", "domain", cert.Domain, "expires_at", cert.ExpiresAt)
		s.markPending(cert)
	}
}

func (s *Service) retryFailed() {
	certs, err := s.controller.ListFailedManagedCertificates()
	if err != nil {
		s.log.Error("error listing failed certificates", "err", err)
		return
	}
	now := s.currentTime()
	for _, cert := range certs {
		if cert == nil {
			continue
		}
		if !s.shouldRetryFailed(cert, now) {
			continue
		}
		s.log.Info("retrying failed certificate", "domain", cert.Domain, "last_error_at", cert.LastErrorAt)
		s.markPending(cert)
	}
}

func (s *Service) retryPending() {
	certs, err := s.controller.ListPendingManagedCertificates()
	if err != nil {
		s.log.Error("error listing pending certificates", "err", err)
		return
	}
	now := s.currentTime()
	for _, cert := range certs {
		if cert == nil {
			continue
		}
		if !s.shouldRetryPending(cert, now) {
			continue
		}
		s.log.Info("retrying stuck pending certificate", "domain", cert.Domain, "updated_at", cert.UpdatedAt)
		s.dispatch(cert)
	}
}

func (s *Service) shouldRetryPending(cert *ct.ManagedCertificate, now time.Time) bool {
	if cert.Status != ct.ManagedCertificateStatusPending {
		return false
	}
	s.handlingMtx.Lock()
	_, busy := s.handling[cert.Domain]
	s.handlingMtx.Unlock()
	if busy {
		return false
	}
	seen := cert.UpdatedAt
	if seen == nil {
		seen = cert.CreatedAt
	}
	if seen == nil {
		return true
	}
	return !now.Before(seen.Add(pendingRetryAge))
}

func (s *Service) shouldRetryFailed(cert *ct.ManagedCertificate, now time.Time) bool {
	if cert.Status != ct.ManagedCertificateStatusFailed {
		return false
	}
	s.handlingMtx.Lock()
	_, busy := s.handling[cert.Domain]
	s.handlingMtx.Unlock()
	if busy {
		return false
	}

	last := cert.LastErrorAt
	if last == nil {
		last = cert.UpdatedAt
	}
	if last == nil {
		return true
	}

	s.failCountMtx.Lock()
	failures := s.failCount[cert.ID]
	s.failCountMtx.Unlock()
	return !now.Before(last.Add(retryBackoff(failures)))
}

func (s *Service) markPending(cert *ct.ManagedCertificate) {
	cert.Status = ct.ManagedCertificateStatusPending
	if err := s.controller.UpdateManagedCertificate(cert); err != nil {
		s.log.Error("error marking certificate pending", "domain", cert.Domain, "id", cert.ID, "err", err)
	}
}

func (s *Service) recordFailure(id string) {
	if id == "" {
		return
	}
	s.failCountMtx.Lock()
	s.failCount[id]++
	s.failCountMtx.Unlock()
}

func (s *Service) clearFailures(id string) {
	if id == "" {
		return
	}
	s.failCountMtx.Lock()
	delete(s.failCount, id)
	s.failCountMtx.Unlock()
}
