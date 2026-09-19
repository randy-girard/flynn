package main

import (
	"net/http"
	"strings"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
	"golang.org/x/net/context"
)

func (c *controllerAPI) CreateSchedulerEvent(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	var ev ct.SchedulerEvent
	if err := httphelper.DecodeJSON(req, &ev); err != nil {
		respondWithError(w, err)
		return
	}
	if strings.TrimSpace(ev.ScheduleID) == "" && strings.TrimSpace(ev.ID) == "" {
		httphelper.ValidationError(w, "schedule_id", "is required")
		return
	}
	if ev.ID == "" {
		ev.ID = random.UUID()
	}
	if ev.ScheduleID == "" {
		ev.ScheduleID = ev.ID
	}
	ev.AppID = app.ID
	if ev.FiredAt.IsZero() {
		ev.FiredAt = time.Now().UTC()
	}
	if err := c.eventRepo.Add(&ct.Event{
		AppID:      app.ID,
		ObjectID:   ev.ScheduleID,
		ObjectType: ct.EventTypeScheduler,
		Op:         ct.EventOpCreate,
	}, ev); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, http.StatusOK, ev)
}
