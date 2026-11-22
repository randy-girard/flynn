package data

import (
	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
)

// TestListCrossAppFormations covers CORE-52: formations and apps.release_id
// rows that point at a release minted for another app (left behind by a
// controller older than CORE-18) are listed, while an app's own releases,
// formations scaled to zero and deleted apps are not.
func (s *S) TestListCrossAppFormations(c *C) {
	apps := NewAppRepo(s.db, "example.com", NewRouteRepo(s.db))
	artifacts := NewArtifactRepo(s.db)
	releases := NewReleaseRepo(s.db, artifacts, nil)
	formations := NewFormationRepo(s.db, apps, releases, artifacts)

	mine := &ct.App{Name: "core52-mine", OwnerAccount: "user:core52-mine"}
	other := &ct.App{Name: "core52-other", OwnerAccount: "user:core52-other"}
	gone := &ct.App{Name: "core52-gone", OwnerAccount: "user:core52-mine"}
	sys := &ct.App{Name: "core52-sys", Meta: map[string]string{"flynn-system-app": "true"}}
	clean := &ct.App{Name: "core52-clean", OwnerAccount: "user:core52-mine"}
	for _, a := range []*ct.App{mine, other, gone, sys, clean} {
		c.Assert(apps.Add(a), IsNil)
	}

	procs := map[string]ct.ProcessType{"web": {}}
	own := &ct.Release{AppID: mine.ID, Processes: procs}
	foreign := &ct.Release{AppID: other.ID, Processes: procs}
	foreignIdle := &ct.Release{AppID: other.ID, Processes: procs}
	cleanOwn := &ct.Release{AppID: clean.ID, Processes: procs}
	for _, r := range []*ct.Release{own, foreign, foreignIdle, cleanOwn} {
		c.Assert(releases.Add(r), IsNil)
	}
	c.Assert(apps.SetRelease(clean, cleanOwn.ID), IsNil)

	scale := func(appID, releaseID string, n int) {
		counts := map[string]int{"web": n}
		_, err := formations.AddScaleRequest(&ct.ScaleRequest{
			AppID:        appID,
			ReleaseID:    releaseID,
			NewProcesses: &counts,
		}, false)
		c.Assert(err, IsNil)
	}
	scale(mine.ID, own.ID, 1)
	scale(mine.ID, foreign.ID, 2)     // listed: cross-app formation with processes
	scale(mine.ID, foreignIdle.ID, 0) // not listed: nothing runs
	scale(other.ID, foreign.ID, 1)    // not listed: other's own release
	scale(gone.ID, foreign.ID, 1)     // not listed once gone is deleted
	scale(sys.ID, foreign.ID, 1)      // listed with System set
	scale(clean.ID, cleanOwn.ID, 1)

	// apps.release_id as an old controller's POST /apps/mine/deploy left it.
	c.Assert(s.db.Exec(`UPDATE apps SET release_id = $1 WHERE app_id = $2`, foreign.ID, mine.ID), IsNil)
	c.Assert(s.db.Exec(`UPDATE apps SET release_id = $1, deleted_at = now() WHERE app_id = $2`, foreign.ID, gone.ID), IsNil)

	rows, err := formations.ListCrossApp()
	c.Assert(err, IsNil)
	type key struct{ kind, app string }
	got := map[key]*ct.CrossAppRelease{}
	for _, row := range rows {
		switch row.AppID {
		case mine.ID, other.ID, gone.ID, sys.ID, clean.ID:
			got[key{row.Kind, row.AppID}] = row
		}
	}
	c.Assert(got, HasLen, 3)

	f := got[key{ct.CrossAppKindFormation, mine.ID}]
	c.Assert(f, NotNil)
	c.Assert(f.AppName, Equals, "core52-mine")
	c.Assert(f.ReleaseID, Equals, foreign.ID)
	c.Assert(f.ReleaseAppID, Equals, other.ID)
	c.Assert(f.ReleaseAppName, Equals, "core52-other")
	c.Assert(f.Processes, DeepEquals, map[string]int{"web": 2})
	c.Assert(f.System, Equals, false)

	cur := got[key{ct.CrossAppKindCurrentRelease, mine.ID}]
	c.Assert(cur, NotNil)
	c.Assert(cur.ReleaseID, Equals, foreign.ID)
	c.Assert(cur.ReleaseAppName, Equals, "core52-other")
	c.Assert(cur.Processes, IsNil)

	sf := got[key{ct.CrossAppKindFormation, sys.ID}]
	c.Assert(sf, NotNil)
	c.Assert(sf.System, Equals, true)

	// Clearing the state as the docs say (scale to zero, deploy the app's own
	// release) empties the list for that app.
	scale(mine.ID, foreign.ID, 0)
	c.Assert(apps.SetRelease(mine, own.ID), IsNil)
	rows, err = formations.ListCrossApp()
	c.Assert(err, IsNil)
	for _, row := range rows {
		c.Assert(row.AppID, Not(Equals), mine.ID)
	}
}
