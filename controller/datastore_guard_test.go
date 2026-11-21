package main

import (
	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/datastoreguard"
	"github.com/randy-girard/flynn/pkg/random"
)

// CORE-37: the app-deletion guard reads every app's owner, current release
// env and attached resources in one query. Another account's env naming the
// datastore does not hold back its delete; the attached consumer does.
func (s *S) TestDatastoreGuardIgnoresOtherTenantEnv(c *C) {
	suffix := random.String(6)
	dsName := "redis-harbor-" + suffix
	ds := s.createTestApp(c, &ct.App{Name: dsName})
	dsRel := s.createTestRelease(c, ds.ID, &ct.Release{Env: map[string]string{"FLYNN_REDIS": dsName}})
	c.Assert(s.c.SetAppRelease(ds.ID, dsRel.ID), IsNil)

	tenantB := s.createTestApp(c, &ct.App{Name: "core37-b-" + suffix, OwnerAccount: "user:core37-b"})
	bRel := s.createTestRelease(c, tenantB.ID, &ct.Release{Env: map[string]string{"X": dsName}})
	c.Assert(s.c.SetAppRelease(tenantB.ID, bRel.ID), IsNil)

	src := data.NewDatastoreRefSource(s.hc.db)
	who, err := datastoreguard.ReferencingApp(src, dsName)
	c.Assert(err, IsNil)
	c.Assert(who, Equals, "")
	c.Assert(datastoreguard.RefuseIfReferenced(src, dsName), IsNil)

	consumer := s.createTestApp(c, &ct.App{Name: "core37-a-" + suffix, OwnerAccount: "user:core37-a"})
	res := s.attachTestDatastore(c, consumer, dsName)
	aRel := s.createTestRelease(c, consumer.ID, &ct.Release{Env: map[string]string{"REDIS_URL": res.Env["REDIS_URL"]}})
	c.Assert(s.c.SetAppRelease(consumer.ID, aRel.ID), IsNil)

	refs, err := src.AppRefs()
	c.Assert(err, IsNil)
	byID := map[string]datastoreguard.AppRef{}
	for _, r := range refs {
		byID[r.ID] = r
	}
	c.Assert(byID[tenantB.ID].OwnerAccount, Equals, "user:core37-b")
	c.Assert(byID[tenantB.ID].Env, DeepEquals, map[string]string{"X": dsName})
	c.Assert(byID[tenantB.ID].ResourceEnvs, HasLen, 0)
	c.Assert(byID[consumer.ID].ResourceEnvs, DeepEquals, []map[string]string{res.Env})
	c.Assert(byID[ds.ID].OwnerAccount, Equals, "")

	who, err = datastoreguard.ReferencingApp(src, dsName)
	c.Assert(err, IsNil)
	c.Assert(who, Equals, consumer.Name)

	_, err = s.c.DeleteResourceApp(res.ProviderID, res.ID, consumer.ID)
	c.Assert(err, IsNil)
	who, err = datastoreguard.ReferencingApp(src, dsName)
	c.Assert(err, IsNil)
	c.Assert(who, Equals, "")
}

// An app with no current release is listed with nil env and does not break
// the scan.
func (s *S) TestDatastoreGuardListsAppsWithoutRelease(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "core37-norel-" + random.String(6)})
	refs, err := data.NewDatastoreRefSource(s.hc.db).AppRefs()
	c.Assert(err, IsNil)
	var found bool
	for _, r := range refs {
		if r.ID == app.ID {
			found = true
			c.Assert(r.Env, IsNil)
			c.Assert(r.Name, Equals, app.Name)
		}
	}
	c.Assert(found, Equals, true)
}
