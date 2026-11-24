package main

import (
	"encoding/json"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
)

// EDGE-75: finished_at and the outcome are one write; a rollback marker
// leaves the deployment unfinished.
func (s *S) TestFinishDeploymentWritesTheOutcomeWithFinishedAt(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "finish-deployment-outcome"})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     app.ID,
		ReleaseID: release.ID,
		Processes: map[string]int{"web": 1},
	}), IsNil)
	defer s.c.DeleteFormation(app.ID, release.ID)
	_, err := s.c.CreateDeployment(app.ID, release.ID)
	c.Assert(err, IsNil)

	newRelease := s.createTestRelease(c, app.ID, &ct.Release{})
	d, err := s.c.CreateDeployment(app.ID, newRelease.ID)
	c.Assert(err, IsNil)
	c.Assert(d.FinishedAt, IsNil)

	_, _, ok, err := data.DeploymentRollbackStarted(s.hc.db, d.ID)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)

	before := time.Now().Add(-time.Minute)
	c.Assert(data.MarkDeploymentRollingBack(s.hc.db, d, "web job failed to start: exit 1"), IsNil)
	got, err := s.c.GetDeployment(d.ID)
	c.Assert(err, IsNil)
	c.Assert(got.FinishedAt, IsNil)
	c.Assert(got.Status, Equals, "running")
	at, msg, ok, err := data.DeploymentRollbackStarted(s.hc.db, d.ID)
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, true)
	c.Assert(msg, Equals, "web job failed to start: exit 1")
	c.Assert(at.After(before), Equals, true)

	finished, err := data.FinishDeployment(s.hc.db, d.ID, "failed", "web job failed to start: exit 1; rollback: x")
	c.Assert(err, IsNil)
	c.Assert(finished, Equals, true)
	got, err = s.c.GetDeployment(d.ID)
	c.Assert(err, IsNil)
	c.Assert(got.FinishedAt, NotNil)
	c.Assert(got.Status, Equals, "failed")

	// A second finish (another worker, the stale-deploy repair) writes
	// nothing: the outcome stays the first one.
	finished, err = data.FinishDeployment(s.hc.db, d.ID, "complete", "")
	c.Assert(err, IsNil)
	c.Assert(finished, Equals, false)
	got, err = s.c.GetDeployment(d.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Status, Equals, "failed")

	events, err := s.c.ListEvents(ct.ListEventsOptions{
		AppID:       app.ID,
		ObjectTypes: []ct.EventType{ct.EventTypeDeployment},
		ObjectID:    d.ID,
	})
	c.Assert(err, IsNil)
	var terminal []string
	for _, e := range events {
		var de ct.DeploymentEvent
		c.Assert(json.Unmarshal(e.Data, &de), IsNil)
		if de.Status == "failed" || de.Status == "complete" {
			terminal = append(terminal, de.Status+": "+de.Error)
		}
	}
	c.Assert(terminal, DeepEquals, []string{"failed: web job failed to start: exit 1; rollback: x"})

	finished, err = data.FinishDeployment(s.hc.db, "00000000-0000-0000-0000-000000000000", "failed", "")
	c.Assert(err, IsNil)
	c.Assert(finished, Equals, false)
}
