package main

import (
	"github.com/randy-girard/flynn/host/cleanup"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/host/volume"
)

// CleanupImageData removes orphaned per-job image material and unreferenced
// layer-cache files on this host's local filesystem. It does not destroy
// persistent ZFS volumes.
func (h *Host) CleanupImageData() error {
	if h == nil || h.state == nil || h.vman == nil {
		return nil
	}
	active := h.state.GetActive()
	jobs := make(map[string]host.ActiveJob, len(active))
	for id, j := range active {
		if j != nil {
			jobs[id] = *j
		}
	}

	vols := h.vman.Volumes()
	volList := make([]*volume.Info, 0, len(vols))
	for _, v := range vols {
		volList = append(volList, v.Info())
	}

	return cleanup.ImageData(h.id, jobs, volList)
}
