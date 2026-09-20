package main

import (
	. "github.com/flynn/go-check"
	"github.com/inconshreveable/log15"
	. "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
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

func (TestSuite) TestMaybePromoteSireniaHASkipsRollingDeploy(c *C) {
	cc := NewFakeControllerClient()
	app := &ct.App{ID: "postgres", Name: "postgres"}
	oldRel := &ct.Release{
		ID:  "pg-old",
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "false"},
	}
	newRel := &ct.Release{
		ID:  "pg-new",
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "false"},
	}
	c.Assert(cc.CreateApp(app), IsNil)
	c.Assert(cc.CreateRelease(app.ID, oldRel), IsNil)
	c.Assert(cc.CreateRelease(app.ID, newRel), IsNil)
	oldProcs := map[string]int{"postgres": 3, "web": 2}
	newProcs := map[string]int{"postgres": 1, "web": 0}
	c.Assert(cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: oldRel.ID, Processes: oldProcs}), IsNil)
	c.Assert(cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: newRel.ID, Processes: newProcs}), IsNil)

	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), log15.New())
	leader := true
	s.isLeader = &leader
	s.formations.Add(NewFormation(&ct.ExpandedFormation{App: app, Release: oldRel, Processes: oldProcs}))
	s.formations.Add(NewFormation(&ct.ExpandedFormation{App: app, Release: newRel, Processes: newProcs}))
	for _, id := range []string{"h1", "h2", "h3"} {
		s.hosts[id] = &Host{ID: id}
	}
	s.jobs["pg-new"] = &Job{
		AppID:     app.ID,
		ReleaseID: newRel.ID,
		Type:      "postgres",
		State:     JobStateRunning,
	}

	s.maybePromoteSireniaHA()

	newForm, err := cc.GetFormation(app.ID, newRel.ID)
	c.Assert(err, IsNil)
	c.Assert(newForm.Processes["postgres"], Equals, 1)
	oldForm, err := cc.GetFormation(app.ID, oldRel.ID)
	c.Assert(err, IsNil)
	c.Assert(oldForm.Processes["postgres"], Equals, 3)
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
