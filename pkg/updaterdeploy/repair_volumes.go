package updaterdeploy

import (
	"fmt"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/volume"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/inconshreveable/log15"
)

// RepairStaleVolumes marks controller volume records destroyed when the
// underlying dataset no longer exists on the assigned host. This can happen
// after manual volume GC or host cleanup while the scheduler still tracks the
// volume, which otherwise causes sirenia rolling deploys to hang until timeout
// with "required volume ... does not exist" on the host.
func RepairStaleVolumes(ctrl controller.Client, hosts []*cluster.Host, log log15.Logger) error {
	if log == nil {
		log = log15.New()
	}

	volumes, err := ctrl.VolumeList()
	if err != nil {
		return fmt.Errorf("list controller volumes: %w", err)
	}

	onHost := hostVolumeIndex(hosts, log)

	var repaired int
	for _, vol := range volumes {
		if vol == nil || vol.ID == "" || vol.HostID == "" {
			continue
		}
		if vol.State == ct.VolumeStateDestroyed || vol.DecommissionedAt != nil {
			continue
		}
		hostVols, ok := onHost[vol.HostID]
		if !ok {
			continue
		}
		if _, exists := hostVols[vol.ID]; exists {
			continue
		}
		log.Warn("marking stale volume destroyed", "vol.id", vol.ID, "host.id", vol.HostID, "app.id", vol.AppID)
		vol.State = ct.VolumeStateDestroyed
		vol.JobID = nil
		if err := ctrl.PutVolume(vol); err != nil {
			return fmt.Errorf("mark volume %s destroyed: %w", vol.ID, err)
		}
		repaired++
	}
	if repaired > 0 {
		log.Info("repaired stale volumes", "count", repaired)
	}
	return nil
}

func hostVolumeIndex(hosts []*cluster.Host, log log15.Logger) map[string]map[string]struct{} {
	index := make(map[string]map[string]struct{}, len(hosts))
	for _, h := range hosts {
		vols, err := h.ListVolumes()
		if err != nil {
			log.Warn("error listing host volumes during repair", "host.id", h.ID(), "err", err)
			continue
		}
		set := make(map[string]struct{}, len(vols))
		for _, info := range vols {
			if isTrackedAppVolume(info) {
				set[info.ID] = struct{}{}
			}
		}
		index[h.ID()] = set
	}
	return index
}

func isTrackedAppVolume(info *volume.Info) bool {
	if info == nil {
		return false
	}
	_, ok := info.Meta["flynn-controller.app"]
	return ok
}
