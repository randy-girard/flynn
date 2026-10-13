package main

import (
	"net/http"
	"strings"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// CreateJobRestartEvent records a job_restart event when the scheduler
// restarts a job that exceeded the cluster job restart interval.
func (c *controllerAPI) CreateJobRestartEvent(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	var ev ct.JobRestartEvent
	if err := httphelper.DecodeJSON(req, &ev); err != nil {
		respondWithError(w, err)
		return
	}
	if strings.TrimSpace(ev.JobID) == "" && strings.TrimSpace(ev.JobUUID) == "" {
		httphelper.ValidationError(w, "job_id", "is required")
		return
	}
	ev.AppID = app.ID
	if ev.AppName == "" {
		ev.AppName = app.Name
	}
	if ev.Reason == "" {
		ev.Reason = ct.JobRestartReasonMaxAge
	}
	if ev.RestartedAt.IsZero() {
		ev.RestartedAt = time.Now().UTC()
	}
	objectID := ev.JobUUID
	if objectID == "" {
		objectID = ev.JobID
	}
	if err := c.eventRepo.Add(&ct.Event{
		AppID:      app.ID,
		ObjectID:   objectID,
		ObjectType: ct.EventTypeJobRestart,
		Op:         ct.EventOpCreate,
	}, ev); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ev)
}
