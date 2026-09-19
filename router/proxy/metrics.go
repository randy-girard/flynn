package proxy

import (
	"math"
	"sort"
	"sync"
	"time"

	router "github.com/randy-girard/flynn/router/types"
)

const latencyWindowSize = 2048

type latencyStore struct {
	mu   sync.Mutex
	byID map[string]*latencyWindow
}

type latencyWindow struct {
	mu      sync.Mutex
	samples []float64
	pos     int
	n       int
	reqs    uint64
	errors  uint64
}

var requestLatencies = newLatencyStore()

func newLatencyStore() *latencyStore {
	return &latencyStore{byID: make(map[string]*latencyWindow)}
}

// Observe records one completed HTTP request's duration for a discoverd service.
func Observe(service string, d time.Duration, status int) {
	if service == "" {
		return
	}
	ms := float64(d) / float64(time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	requestLatencies.observe(service, ms, status)
}

// Snapshot returns current request-latency percentiles per service.
func Snapshot() []router.ServiceMetrics {
	return requestLatencies.snapshot()
}

func (s *latencyStore) observe(service string, ms float64, status int) {
	s.mu.Lock()
	w := s.byID[service]
	if w == nil {
		w = &latencyWindow{samples: make([]float64, latencyWindowSize)}
		s.byID[service] = w
	}
	s.mu.Unlock()

	w.observe(ms, status)
}

func (w *latencyWindow) observe(ms float64, status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.samples[w.pos] = ms
	w.pos = (w.pos + 1) % len(w.samples)
	if w.n < len(w.samples) {
		w.n++
	}
	w.reqs++
	if status >= 500 || status == 499 {
		w.errors++
	}
}

func (s *latencyStore) snapshot() []router.ServiceMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]router.ServiceMetrics, 0, len(s.byID))
	for name, w := range s.byID {
		out = append(out, w.snapshot(name))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}

func (w *latencyWindow) snapshot(service string) router.ServiceMetrics {
	w.mu.Lock()
	n := w.n
	vals := make([]float64, n)
	if n == len(w.samples) {
		copy(vals, w.samples)
	} else {
		copy(vals, w.samples[:n])
	}
	reqs, errs := w.reqs, w.errors
	w.mu.Unlock()
	sort.Float64s(vals)
	return router.ServiceMetrics{
		Service:   service,
		Requests:  reqs,
		Errors:    errs,
		P50Millis: round2(percentile(vals, 50)),
		P95Millis: round2(percentile(vals, 95)),
		P99Millis: round2(percentile(vals, 99)),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := (p / 100) * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if hi >= len(sorted) {
		return sorted[lo]
	}
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
