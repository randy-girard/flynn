package router

import (
	"math"
	"sort"
)

// Percentile returns the linearly interpolated percentile of a sorted slice.
// p is in 0–100. Empty input is 0.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
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

// ComputePercentiles sorts a copy of vals and returns p50 / p95 / p99 in ms.
func ComputePercentiles(vals []float64) (p50, p95, p99 float64) {
	if len(vals) == 0 {
		return 0, 0, 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	return round2(Percentile(sorted, 50)), round2(Percentile(sorted, 95)), round2(Percentile(sorted, 99))
}

// MergeServiceMetrics combines snapshots from every router instance into one
// row per service. Percentiles are recomputed from the concatenated sample
// windows so they reflect cluster-wide traffic, not a single router.
func MergeServiceMetrics(parts [][]ServiceMetrics) []ServiceMetrics {
	type acc struct {
		samples []float64
		reqs    uint64
		errors  uint64
	}
	by := make(map[string]*acc)
	var order []string
	for _, list := range parts {
		for _, row := range list {
			name := row.Service
			if name == "" {
				continue
			}
			a := by[name]
			if a == nil {
				a = &acc{}
				by[name] = a
				order = append(order, name)
			}
			if len(row.Samples) > 0 {
				a.samples = append(a.samples, row.Samples...)
			} else if row.Requests > 0 || row.P50Millis > 0 || row.P95Millis > 0 || row.P99Millis > 0 {
				// Older router with no sample payload: keep the published
				// percentiles in the mix rather than dropping that node's traffic.
				a.samples = append(a.samples, row.P50Millis, row.P95Millis, row.P99Millis)
			}
			a.reqs += row.Requests
			a.errors += row.Errors
		}
	}
	out := make([]ServiceMetrics, 0, len(order))
	for _, name := range order {
		a := by[name]
		p50, p95, p99 := ComputePercentiles(a.samples)
		reqs := a.reqs
		if reqs == 0 {
			reqs = uint64(len(a.samples))
		}
		out = append(out, ServiceMetrics{
			Service:   name,
			Requests:  reqs,
			Errors:    a.errors,
			P50Millis: p50,
			P95Millis: p95,
			P99Millis: p99,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}
