package main

import (
	. "github.com/flynn/flynn/controller/testutils"
	ct "github.com/flynn/flynn/controller/types"
	. "github.com/flynn/go-check"
	"github.com/inconshreveable/log15"
)

func (TestSuite) TestMaybePromoteSireniaHA(c *C) {
	cc := NewFakeControllerClient()
	app := &ct.App{ID: "postgres", Name: "postgres"}
	release := &ct.Release{
		ID:  "pg-single",
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true"},
	}
	c.Assert(cc.CreateApp(app), IsNil)
	c.Assert(cc.CreateRelease(app.ID, release), IsNil)
	procs := map[string]int{"postgres": 1, "web": 1}
	c.Assert(cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: procs}), IsNil)

	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), log15.New())
	leader := true
	s.isLeader = &leader
	s.formations.Add(NewFormation(&ct.ExpandedFormation{
		App:       app,
		Release:   release,
		Processes: procs,
	}))
	for _, id := range []string{"h1", "h2", "h3"} {
		s.hosts[id] = &Host{ID: id}
	}

	s.maybePromoteSireniaHA()

	active, err := cc.FormationListActive()
	c.Assert(err, IsNil)
	c.Assert(active, HasLen, 1)
	c.Assert(active[0].Release.Env["SINGLETON"], Equals, "false")
	c.Assert(active[0].Release.ID, Not(Equals), release.ID)
	c.Assert(active[0].Processes["postgres"], Equals, 1)
	c.Assert(active[0].Processes["web"], Equals, 1)

	oldForm, err := cc.GetFormation(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(oldForm.Processes["postgres"], Equals, 0)

	// scale-up waits until the env-flipped primary is running
	s.maybePromoteSireniaHA()
	active, err = cc.FormationListActive()
	c.Assert(err, IsNil)
	c.Assert(active[0].Processes["postgres"], Equals, 1)

	s.jobs["pg-new"] = &Job{
		AppID:     app.ID,
		ReleaseID: active[0].Release.ID,
		Type:      "postgres",
		State:     JobStateRunning,
	}
	s.maybePromoteSireniaHA()
	active, err = cc.FormationListActive()
	c.Assert(err, IsNil)
	c.Assert(active[0].Processes["postgres"], Equals, 3)
	c.Assert(active[0].Processes["web"], Equals, 2)
}

func (TestSuite) TestMaybePromoteSireniaHARequiresThreeHosts(c *C) {
	cc := NewFakeControllerClient()
	app := &ct.App{ID: "postgres", Name: "postgres"}
	release := &ct.Release{
		ID:  "pg-single",
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true"},
	}
	c.Assert(cc.CreateApp(app), IsNil)
	c.Assert(cc.CreateRelease(app.ID, release), IsNil)
	procs := map[string]int{"postgres": 1}
	c.Assert(cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: procs}), IsNil)

	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), log15.New())
	leader := true
	s.isLeader = &leader
	s.formations.Add(NewFormation(&ct.ExpandedFormation{
		App:       app,
		Release:   release,
		Processes: procs,
	}))
	s.hosts["h1"] = &Host{ID: "h1"}
	s.hosts["h2"] = &Host{ID: "h2"}

	s.maybePromoteSireniaHA()
	active, err := cc.FormationListActive()
	c.Assert(err, IsNil)
	c.Assert(active, HasLen, 1)
	c.Assert(active[0].Release.Env["SINGLETON"], Equals, "true")
}

func (TestSuite) TestMaybePromoteSireniaHARequiresLeader(c *C) {
	cc := NewFakeControllerClient()
	app := &ct.App{ID: "postgres", Name: "postgres"}
	release := &ct.Release{
		ID:  "pg-single",
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true"},
	}
	c.Assert(cc.CreateApp(app), IsNil)
	c.Assert(cc.CreateRelease(app.ID, release), IsNil)
	procs := map[string]int{"postgres": 1}
	c.Assert(cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: procs}), IsNil)

	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(false), log15.New())
	leader := false
	s.isLeader = &leader
	s.formations.Add(NewFormation(&ct.ExpandedFormation{
		App:       app,
		Release:   release,
		Processes: procs,
	}))
	for _, id := range []string{"h1", "h2", "h3"} {
		s.hosts[id] = &Host{ID: id}
	}

	s.maybePromoteSireniaHA()
	active, err := cc.FormationListActive()
	c.Assert(err, IsNil)
	c.Assert(active, HasLen, 1)
	c.Assert(active[0].Release.Env["SINGLETON"], Equals, "true")
}
