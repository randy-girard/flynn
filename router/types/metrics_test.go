package router

import "testing"

func TestPercentileLinearInterpolation(t *testing.T) {
	got := Percentile([]float64{10, 20, 30, 40}, 50)
	if got != 25 {
		t.Fatalf("p50 = %v, want 25", got)
	}
	if Percentile(nil, 95) != 0 {
		t.Fatal("empty window should be 0")
	}
	if Percentile([]float64{7}, 99) != 7 {
		t.Fatal("single sample should return itself")
	}
}

func TestMergeServiceMetricsRecomputesPercentiles(t *testing.T) {
	a := []ServiceMetrics{{
		Service:  "demo-web",
		Requests: 3,
		Samples:  []float64{10, 10, 10},
	}}
	b := []ServiceMetrics{{
		Service:  "demo-web",
		Requests: 1,
		Samples:  []float64{40},
	}}
	got := MergeServiceMetrics([][]ServiceMetrics{a, b})
	if len(got) != 1 || got[0].Service != "demo-web" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Requests != 4 {
		t.Fatalf("requests = %d, want 4", got[0].Requests)
	}
	// Combined population 10,10,10,40 → p50 interpolates between 10 and 10.
	if got[0].P50Millis != 10 {
		t.Fatalf("p50 = %v, want 10", got[0].P50Millis)
	}
	if got[0].P99Millis <= got[0].P50Millis {
		t.Fatalf("p99 should sit at the 40ms tail: %+v", got[0])
	}
	if len(got[0].Samples) != 0 {
		t.Fatalf("merged public snapshot must omit raw samples: %+v", got[0])
	}
}

func TestComputePercentilesKnownSet(t *testing.T) {
	p50, p95, p99 := ComputePercentiles([]float64{5, 5, 5, 5, 100})
	if p50 != 5 {
		t.Fatalf("p50 = %v, want 5", p50)
	}
	if p95 < 5 || p99 < p95 {
		t.Fatalf("p95=%v p99=%v", p95, p99)
	}
}
