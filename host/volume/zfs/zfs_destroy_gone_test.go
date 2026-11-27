package zfs

import (
	. "github.com/flynn/go-check"
	gzfs "github.com/mistifyio/go-zfs"
	"github.com/randy-girard/flynn/pkg/testutils"
)

type ZfsDestroyGoneTests struct {
	TempZpool
}

var _ = Suite(&ZfsDestroyGoneTests{})

func (ZfsDestroyGoneTests) SetUpSuite(c *C) {
	testutils.SkipIfNotRoot(c)
}

// NEW-35: the scheduler destroys decommissioned volumes and retries until a
// destroy succeeds. A dataset that is already gone (an operator's zfs
// destroy, an earlier attempt that died after ZFS removed it) must count as
// destroyed, or the host keeps a record it can never remove.
func (s *ZfsDestroyGoneTests) TestDestroyVolumeWhoseDatasetIsGone(c *C) {
	v, err := s.VolProv.NewVolume(nil)
	c.Assert(err, IsNil)
	zv := v.(*zfsVolume)
	ds, err := gzfs.GetDataset(zv.dataset.Name)
	c.Assert(err, IsNil)
	c.Assert(ds.Destroy(gzfs.DestroyForceUmount), IsNil)

	c.Assert(s.VolProv.DestroyVolume(v), IsNil)
	_, ok := s.VolProv.(*Provider).volumes[v.Info().ID]
	c.Assert(ok, Equals, false)
}
