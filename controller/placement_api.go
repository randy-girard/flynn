package main

import (
	"net/http"

	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetClusterPlacement(_ context.Context, w http.ResponseWriter, _ *http.Request) {
	mode, err := c.workloadPlacement()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ct.WorkloadPlacement{Mode: mode})
}

func (c *controllerAPI) workloadPlacement() (string, error) {
	if c == nil || c.tenancy == nil {
		return placement.ModeMixed, nil
	}
	mode, err := c.tenancy.GetWorkloadPlacement()
	if err != nil {
		if err == data.ErrNotFound {
			return placement.ModeMixed, nil
		}
		return "", err
	}
	if mode == "" {
		return placement.ModeMixed, nil
	}
	return mode, nil
}

func (c *controllerAPI) PutClusterPlacement(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := httphelper.DecodeJSON(req, &body); err != nil {
		respondWithError(w, err)
		return
	}
	if err := placement.ValidateMode(body.Mode); err != nil {
		httphelper.ValidationError(w, "mode", err.Error())
		return
	}
	if body.Mode == placement.ModeSplit {
		if err := c.rejectSplitWithoutCore(); err != nil {
			respondWithError(w, err)
			return
		}
	}
	if c.tenancy == nil {
		respondWithError(w, data.ErrNotFound)
		return
	}
	if err := c.tenancy.SetWorkloadPlacement(body.Mode); err != nil {
		respondWithError(w, err)
		return
	}
	_ = c.tenancy.Audit(c.actor(ctx), "placement.update", "", "", body)
	c.GetClusterPlacement(ctx, w, req)
}

// rejectSplitWithoutCore refuses split unless a host that is not shutting
// down has flynn-role=core. Zero app hosts is allowed. A host-list error
// refuses the change: the controller cannot prove a core host exists.
func (c *controllerAPI) rejectSplitWithoutCore() error {
	if c == nil || c.clusterClient == nil {
		return ct.ValidationError{Field: "mode", Message: "refusing split: cluster hosts are unavailable"}
	}
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		return err
	}
	views := make([]placement.Host, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		views = append(views, placementHost(h))
	}
	if !placement.HasCore(views) {
		return ct.ValidationError{
			Field:   "mode",
			Message: "refusing split unless a host that is not shutting down has flynn-role=core",
		}
	}
	return nil
}

func placementHost(h utils.HostClient) placement.Host {
	view := placement.Host{Tags: h.Tags()}
	if s, ok := h.(interface{ ShuttingDown() bool }); ok {
		view.Shutdown = s.ShuttingDown()
	}
	return view
}
