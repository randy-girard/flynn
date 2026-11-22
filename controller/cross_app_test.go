package main

import (
	. "github.com/flynn/go-check"
	v1controller "github.com/randy-girard/flynn/controller/client/v1"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// CORE-52: GET /cluster/cross-app-releases lists formations and current
// releases left pointing at another app's release by a controller older than
// CORE-18. It is for the cluster key or a cluster admin only.
func (s *S) TestListCrossAppReleases(c *C) {
	f := s.newOwnershipFixture(c, "core52")
	// A cluster admin can still write a cross-app formation (operator
	// repair); an old controller let the tenant do it. The current release
	// is set in the database the way an old POST /apps/mine/deploy left it.
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     f.mine.ID,
		ReleaseID: f.otherRel.ID,
		Processes: map[string]int{"web": 1},
	}), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE apps SET release_id = $1 WHERE app_id = $2`, f.otherRel.ID, f.mine.ID), IsNil)

	var rows []*ct.CrossAppRelease
	c.Assert(s.c.(*v1controller.Client).Get("/cluster/cross-app-releases", &rows), IsNil)
	kinds := map[string]*ct.CrossAppRelease{}
	for _, row := range rows {
		if row.AppID == f.mine.ID {
			kinds[row.Kind] = row
		}
	}
	c.Assert(kinds, HasLen, 2)
	c.Assert(kinds[ct.CrossAppKindFormation].ReleaseID, Equals, f.otherRel.ID)
	c.Assert(kinds[ct.CrossAppKindFormation].ReleaseAppName, Equals, f.other.Name)
	c.Assert(kinds[ct.CrossAppKindFormation].Processes, DeepEquals, map[string]int{"web": 1})
	c.Assert(kinds[ct.CrossAppKindCurrentRelease].ReleaseID, Equals, f.otherRel.ID)

	// An app-scoped tenant cannot list the cluster's cross-app state.
	var denied []*ct.CrossAppRelease
	err := f.http().Get("/cluster/cross-app-releases", &denied)
	c.Assert(err, NotNil)
	c.Assert(httphelper.IsForbidden(err), Equals, true, Commentf("%v", err))
	c.Assert(denied, HasLen, 0)

	// The documented clean-up: scale the foreign formation to zero and
	// deploy the app's own release.
	c.Assert(s.c.PutFormation(&ct.Formation{
		AppID:     f.mine.ID,
		ReleaseID: f.otherRel.ID,
		Processes: map[string]int{"web": 0},
	}), IsNil)
	c.Assert(s.c.SetAppRelease(f.mine.ID, f.mineRel.ID), IsNil)
	rows = nil
	c.Assert(s.c.(*v1controller.Client).Get("/cluster/cross-app-releases", &rows), IsNil)
	for _, row := range rows {
		c.Assert(row.AppID, Not(Equals), f.mine.ID)
	}
}
