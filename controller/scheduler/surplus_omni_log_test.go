package main

import (
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

// A fresh flannel job on a host was stopped within milliseconds as
// scale-down during `update`. The scheduler log only said "detected 1
// surplus omni jobs", so it was not possible to tell whether the scheduler
// held a stale copy or another live job on the host. The stop must name the
// victim and every job it was counted against, with the scheduler's state
// for each.
func TestStopSurplusOmniJobsLogsWhatItCountedAgainst(t *testing.T) {
	formation := NewFormation(&ct.ExpandedFormation{
		App:       &ct.App{ID: "flannel"},
		Release:   &ct.Release{ID: "r1", Processes: map[string]ct.ProcessType{"app": {Omni: true}}},
		Processes: Processes{"app": 1},
	})
	old := &Job{ID: "old", HostID: "h2", Type: "app", State: JobStateRunning, Formation: formation, StartedAt: time.Now().Add(-time.Minute)}
	fresh := &Job{ID: "fresh", HostID: "h2", Type: "app", State: JobStateStarting, Formation: formation, Reason: "restart"}

	var records []*log15.Record
	logger := log15.New()
	logger.SetHandler(log15.FuncHandler(func(r *log15.Record) error {
		records = append(records, r)
		return nil
	}))
	s := &Scheduler{
		isLeader: typeconv.BoolPtr(true),
		jobs:     Jobs{"old": old, "fresh": fresh},
		hosts:    map[string]*Host{},
		logger:   logger,
	}
	s.stopSurplusOmniJobs(formation)

	for _, r := range records {
		if r.Msg != "stopping surplus omni job" {
			continue
		}
		ctx := map[string]interface{}{}
		for i := 0; i+1 < len(r.Ctx); i += 2 {
			ctx[r.Ctx[i].(string)] = r.Ctx[i+1]
		}
		if ctx["job.id"] != "fresh" {
			t.Fatalf("stopped %v, want fresh", ctx["job.id"])
		}
		counted, _ := ctx["counted"].([]string)
		joined := strings.Join(counted, ",")
		if !strings.Contains(joined, "old:running:") || !strings.Contains(joined, "fresh:starting:unstarted") {
			t.Fatalf("counted = %v, want both jobs with state", counted)
		}
		return
	}
	t.Fatal("no \"stopping surplus omni job\" record; the scale-down is not explained in the log")
}
