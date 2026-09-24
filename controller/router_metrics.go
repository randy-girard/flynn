package main

import (
	"net/http"
	"sync"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/httphelper"
	routerc "github.com/randy-girard/flynn/router/client"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

// GetRouterMetrics returns cluster-wide HTTP latency percentiles.
// It fans out to every router-api instance and recomputes p50/p95/p99 from
// the combined recent sample windows so the dashboard matches live traffic
// rather than a single router's stale ring buffer.
func (c *controllerAPI) GetRouterMetrics(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	list, err := collectRouterMetrics()
	if err != nil {
		logger.Warn("failed to get router metrics", "error", err)
		httphelper.JSON(w, 200, []router.ServiceMetrics{})
		return
	}
	httphelper.JSON(w, 200, list)
}

func collectRouterMetrics() ([]router.ServiceMetrics, error) {
	httpc := &http.Client{Timeout: 4 * time.Second}
	insts, err := discoverd.GetInstances("router-api", 2*time.Second)
	if err != nil || len(insts) == 0 {
		client := routerc.NewWithHTTP(httpc)
		list, err := client.GetMetricsWithSamples()
		if err != nil {
			return nil, err
		}
		return router.MergeServiceMetrics([][]router.ServiceMetrics{list}), nil
	}

	parts := make([][]router.ServiceMetrics, len(insts))
	var wg sync.WaitGroup
	for i, inst := range insts {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			client := routerc.NewWithAddr(addr)
			list, err := client.GetMetricsWithSamples()
			if err != nil {
				logger.Warn("failed to get router metrics from instance", "addr", addr, "error", err)
				return
			}
			parts[i] = list
		}(i, inst.Addr)
	}
	wg.Wait()
	return router.MergeServiceMetrics(parts), nil
}
