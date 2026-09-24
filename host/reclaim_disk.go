package main

import (
	"github.com/randy-girard/flynn/host/cleanup"
	zfsVolume "github.com/randy-girard/flynn/host/volume/zfs"
)

var trimPool = cleanup.TrimZpool

// ReclaimDisk deletes leftover image dirs / unreferenced layer-cache files,
// then TRIM the local ZFS pool so a sparse file vdev can return unused
// extents to the host root filesystem. It does not destroy persistent volumes;
// callers run volume GC first.
func (h *Host) ReclaimDisk() error {
	if err := h.CleanupImageData(); err != nil {
		return err
	}
	pool := h.zpoolName
	if pool == "" {
		pool = zfsVolume.DefaultDatasetName
	}
	return trimPool(pool)
}
