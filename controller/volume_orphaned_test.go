package main

import (
	"time"

	. "github.com/flynn/go-check"
	c1 "github.com/randy-girard/flynn/controller/client/v1"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/random"
)

// TestOrphanedVolumeList is the NEW-35 cleanup listing: volumes of apps that
// were deleted before app deletion decommissioned volumes are still live in
// the table. GET /volumes?orphaned=true lists them for flynn-host volume:gc
// --deleted-apps; a grace keeps recently deleted apps out.
func (s *S) TestOrphanedVolumeList(c *C) {
	live := s.createTestApp(c, &ct.App{Name: "orphan-live-" + random.String(6)})
	gone := s.createTestApp(c, &ct.App{Name: "orphan-gone-" + random.String(6)})
	put := func(appID string) *ct.Volume {
		vol := &ct.Volume{
			VolumeReq: ct.VolumeReq{Path: "/data"},
			ID:        random.UUID(),
			HostID:    "host1",
			Type:      volume.VolumeTypeData,
			State:     ct.VolumeStateCreated,
			AppID:     appID,
			ReleaseID: random.UUID(),
		}
		c.Assert(s.c.PutVolume(vol), IsNil)
		return vol
	}
	liveVol := put(live.ID)
	goneVol := put(gone.ID)
	c.Assert(s.hc.db.Exec("app_delete", gone.ID), IsNil)
	c.Assert(s.hc.db.Exec("UPDATE apps SET deleted_at = now() - interval '2 hours' WHERE app_id = $1", gone.ID), IsNil)

	client := s.c.(*c1.Client)
	list, err := client.OrphanedVolumeList(time.Hour)
	c.Assert(err, IsNil)
	ids := map[string]bool{}
	for _, v := range list {
		ids[v.ID] = true
	}
	c.Assert(ids[goneVol.ID], Equals, true)
	c.Assert(ids[liveVol.ID], Equals, false)

	list, err = client.OrphanedVolumeList(3 * time.Hour)
	c.Assert(err, IsNil)
	for _, v := range list {
		c.Assert(v.ID, Not(Equals), goneVol.ID)
	}
}
