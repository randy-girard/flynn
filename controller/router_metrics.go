package main

import (
	"net/http"
	"time"

	"github.com/randy-girard/flynn/pkg/httphelper"
	routerc "github.com/randy-girard/flynn/router/client"
	router "github.com/randy-girard/flynn/router/types"
	"golang.org/x/net/context"
)

// GetRouterMetrics proxies recent HTTP latency percentiles from router-api.
// Failures return an empty list so dashboards can treat router metrics as
// best-effort rather than blocking the rest of the metrics page.
func (c *controllerAPI) GetRouterMetrics(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	client := routerc.NewWithHTTP(&http.Client{Timeout: 4 * time.Second})
	list, err := client.GetMetrics()
	if err != nil {
		logger.Warn("failed to get router metrics", "error", err)
		httphelper.JSON(w, 200, []router.ServiceMetrics{})
		return
	}
	if list == nil {
		list = []router.ServiceMetrics{}
	}
	httphelper.JSON(w, 200, list)
}
