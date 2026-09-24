package proxy

import (
	"sort"
	"sync"
	"time"

	router "github.com/randy-girard/flynn/router/types"
)

const (
	latencyWindowSize = 4096
	latencyMaxAge     = 60 * time.Second
)

type latencySample struct {
	at  time.Time
	ms  float64
	err bool
}

type latencyStore struct {
	mu   sync.Mutex
	byID map[string]*latencyWindow
}

type latencyWindow struct {
	mu      sync.Mutex
	samples []latencySample
	pos     int
	n       int
}

var requestLatencies = newLatencyStore()

func newLatencyStore() *latencyStore {
	return &latencyStore{byID: make(map[string]*latencyWindow)}
}

// Observe records one completed HTTP request's backend response time (TTFB)
// for a discoverd service, in milliseconds.
func Observe(service string, d time.Duration, status int) {
	ObserveAt(service, d, status, time.Now())
}

// ObserveAt is Observe with a caller-supplied timestamp (tests).
func ObserveAt(service string, d time.Duration, status int, at time.Time) {
	if service == "" {
		return
	}
	ms := float64(d) / float64(time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	if at.IsZero() {
		at = time.Now()
	}
	requestLatencies.observe(service, ms, status, at)
}

// Snapshot returns current request-latency percentiles per service, using only
// samples from the last minute so the values match live traffic.
func Snapshot() []router.ServiceMetrics {
	return requestLatencies.snapshot(false, time.Now())
}

// SnapshotWithSamples is Snapshot plus the in-window sample list, for merging
// across router instances.
func SnapshotWithSamples() []router.ServiceMetrics {
	return requestLatencies.snapshot(true, time.Now())
}

func (s *latencyStore) observe(service string, ms float64, status int, at time.Time) {
	s.mu.Lock()
	w := s.byID[service]
	if w == nil {
		w = &latencyWindow{samples: make([]latencySample, latencyWindowSize)}
		s.byID[service] = w
	}
	s.mu.Unlock()

	w.observe(latencySample{at: at, ms: ms, err: status >= 500 || status == 499})
}

func (w *latencyWindow) observe(s latencySample) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples[w.pos] = s
	w.pos = (w.pos + 1) % len(w.samples)
	if w.n < len(w.samples) {
		w.n++
	}
}

func (s *latencyStore) snapshot(includeSamples bool, now time.Time) []router.ServiceMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]router.ServiceMetrics, 0, len(s.byID))
	for name, w := range s.byID {
		row, ok := w.snapshot(name, includeSamples, now, latencyMaxAge)
		if !ok {
			continue
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func (w *latencyWindow) snapshot(service string, includeSamples bool, now time.Time, maxAge time.Duration) (router.ServiceMetrics, bool) {
	w.mu.Lock()
	n := w.n
	capn := len(w.samples)
	pos := w.pos
	cutoff := now.Add(-maxAge)
	vals := make([]float64, 0, n)
	var errs uint64
	for i := 0; i < n; i++ {
		idx := i
		if n == capn {
			idx = (pos + i) % capn
		}
		s := w.samples[idx]
		if s.at.Before(cutoff) {
			continue
		}
		vals = append(vals, s.ms)
		if s.err {
			errs++
		}
	}
	w.mu.Unlock()
	if len(vals) == 0 {
		return router.ServiceMetrics{}, false
	}
	p50, p95, p99 := router.ComputePercentiles(vals)
	row := router.ServiceMetrics{
		Service:   service,
		Requests:  uint64(len(vals)),
		Errors:    errs,
		P50Millis: p50,
		P95Millis: p95,
		P99Millis: p99,
	}
	if includeSamples {
		row.Samples = vals
	}
	return row, true
}
