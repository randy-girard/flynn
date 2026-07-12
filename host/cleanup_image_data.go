package main

import (
	"github.com/flynn/flynn/host/cleanup"
	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume"
)

// CleanupImageData removes orphaned per-job image material and unreferenced
// layer-cache files on this host's local filesystem.
func (h *Host) CleanupImageData() error {
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
