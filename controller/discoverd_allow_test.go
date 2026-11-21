package main

import (
	"time"

	. "github.com/flynn/go-check"
	tu "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/netpolicy"
	"github.com/randy-girard/flynn/pkg/random"
)

// attachTestDatastore records a resource row for app with the env a
// datastore provider returns. Only the cluster key may PUT a resource.
func (s *S) attachTestDatastore(c *C, app *ct.App, name string) *ct.Resource {
	provider := s.createTestProvider(c, &ct.Provider{URL: "https://example.com/" + random.String(12), Name: "p-" + random.String(8)})
	res := &ct.Resource{
		ID:         random.UUID(),
		ProviderID: provider.ID,
		ExternalID: "/instances/" + name,
		Env: map[string]string{
			"FLYNN_REDIS": name,
			"REDIS_URL":   "redis://:pw@leader." + name + ".discoverd:6379",
		},
		Apps: []string{app.ID},
	}
	c.Assert(s.c.PutResource(res), IsNil)
	return res
}

// TestExpandedFormationDiscoverdAllowFromResources: the allowlist the
// scheduler stamps comes from resource rows attached to the app. A tenant
// release env naming another datastore does not add it.
func (s *S) TestExpandedFormationDiscoverdAllowFromResources(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "dns-allow-" + random.String(6)})
	s.attachTestDatastore(c, app, "redis-lagoon-59415")
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Env:       map[string]string{"X": "leader.redis-victim-11111.discoverd"},
		Processes: map[string]ct.ProcessType{"web": {Args: []string{"run"}}},
	})
	// Active formations would leak into TestFormationListActive.
	defer s.deleteTestFormation(s.createTestFormation(c, &ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 1}}))

	ef, err := s.c.GetExpandedFormation(app.ID, release.ID)
	c.Assert(err, IsNil)
	c.Assert(ef.DiscoverdAllow, DeepEquals, []string{"redis-lagoon-59415"})

	var found bool
	list, err := s.api.formationRepo.ListActive()
	c.Assert(err, IsNil)
	for _, f := range list {
		if f.App.ID == app.ID {
			found = true
			c.Assert(f.DiscoverdAllow, DeepEquals, []string{"redis-lagoon-59415"})
		}
	}
	c.Assert(found, Equals, true)
	since, err := s.api.formationRepo.ListSince(time.Now().Add(-time.Hour))
	c.Assert(err, IsNil)
	for _, f := range since {
		if f.App.ID == app.ID {
			c.Assert(f.DiscoverdAllow, DeepEquals, []string{"redis-lagoon-59415"})
		}
	}

	// An app with nothing attached gets an empty (not nil) list, so jobs
	// are stamped and the host does not fall back to env.
	bare := s.createTestApp(c, &ct.App{Name: "dns-allow-bare-" + random.String(6)})
	bareRelease := s.createTestRelease(c, bare.ID, &ct.Release{
		Env:       map[string]string{"REDIS_URL": "redis://leader.redis-victim-22222.discoverd:6379"},
		Processes: map[string]ct.ProcessType{"web": {Args: []string{"run"}}},
	})
	defer s.deleteTestFormation(s.createTestFormation(c, &ct.Formation{AppID: bare.ID, ReleaseID: bareRelease.ID, Processes: map[string]int{"web": 1}}))
	ef, err = s.c.GetExpandedFormation(bare.ID, bareRelease.ID)
	c.Assert(err, IsNil)
	c.Assert(ef.DiscoverdAllow, NotNil)
	c.Assert(ef.DiscoverdAllow, HasLen, 0)
}

// TestRunJobStampsDiscoverdAllowFromResources: one-off env (flynn run -e)
// and release env are tenant input; the stamp lists only attached resources,
// and a tenant cannot pass its own value for the reserved key.
func (s *S) TestRunJobStampsDiscoverdAllowFromResources(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "dns-allow-run-" + random.String(6)})
	s.attachTestDatastore(c, app, "redis-lagoon-59415")
	artifact := s.createTestArtifact(c, &ct.Artifact{})
	hc := tu.NewFakeHostClient(fakeHostID(), false)
	s.cc.AddHost(hc)
	release := s.createTestRelease(c, app.ID, &ct.Release{
		ArtifactIDs: []string{artifact.ID},
		Env:         map[string]string{"X": "leader.redis-victim-11111.discoverd"},
	})

	_, err := s.api.startDetachedJob(app, &ct.NewJob{
		ReleaseID:  release.ID,
		ReleaseEnv: true,
		Args:       []string{"true"},
		Env:        map[string]string{"Y": "leader.redis-victim-22222.discoverd"},
		Meta:       map[string]string{netpolicy.MetaJobDiscoverdAllow: "redis-victim-33333"},
	})
	c.Assert(err, IsNil)
	jobs, err := hc.ListJobs()
	c.Assert(err, IsNil)
	c.Assert(jobs, HasLen, 1)
	for _, j := range jobs {
		c.Assert(j.Job.Metadata[netpolicy.MetaJobDiscoverdAllow], Equals, "redis-lagoon-59415")
	}
}
