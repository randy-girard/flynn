package main

import (
	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/random"
)

// GET /volumes and GET /apps/:id/volumes return the size the scheduler
// persisted (NEW-28). It used to be dropped on the way into the table.
func (s *S) TestVolumeListIncludesSize(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "vol-size-" + random.String(6)})
	const size int64 = 2 * 1024 * 1024 * 1024
	vol := &ct.Volume{
		VolumeReq: ct.VolumeReq{Path: "/data", Size: size},
		ID:        random.UUID(),
		HostID:    "host1",
		Type:      volume.VolumeTypeData,
		State:     ct.VolumeStateCreated,
		AppID:     app.ID,
		ReleaseID: random.UUID(),
	}
	c.Assert(s.c.PutVolume(vol), IsNil)

	got, err := s.c.GetVolume(app.ID, vol.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Size, Equals, size)

	list, err := s.c.AppVolumeList(app.ID)
	c.Assert(err, IsNil)
	c.Assert(list, HasLen, 1)
	c.Assert(list[0].Size, Equals, size)

	all, err := s.c.VolumeList()
	c.Assert(err, IsNil)
	var found bool
	for _, v := range all {
		if v.ID == vol.ID {
			found = true
			c.Assert(v.Size, Equals, size)
		}
	}
	c.Assert(found, Equals, true)
}
