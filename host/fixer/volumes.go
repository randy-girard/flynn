package fixer

import (
	"fmt"
	"strings"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/volume"
)

var sireniaDBApps = []string{"postgres", "mariadb", "mongodb"}

// FixStaleControllerVolumes decommissions controller volume records that are
// missing from every host. This only updates controller metadata; it does not
// delete volume data from hosts.
func (f *ClusterFixer) FixStaleControllerVolumes(c controller.Client) error {
	log := f.l.New("fn", "FixStaleControllerVolumes")
	hostVolIDs, err := f.collectHostVolumeIDs()
	if err != nil {
		return err
	}
	apps, err := c.AppList()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	appIDs := make(map[string]string, len(sireniaDBApps))
	for _, a := range apps {
		for _, name := range sireniaDBApps {
			if a.Name == name {
				appIDs[name] = a.ID
			}
		}
	}
	var decommissioned int
	for _, name := range sireniaDBApps {
		appID, ok := appIDs[name]
		if !ok {
			continue
		}
		vols, err := c.AppVolumeList(appID)
		if err != nil {
			log.Error("list app volumes", "app", name, "err", err)
			continue
		}
		for _, vol := range vols {
			if !shouldDecommissionStaleVolume(vol, hostVolIDs) {
				continue
			}
			log.Info("decommissioning stale controller volume", "app", name, "volume", vol.ID, "host_id", vol.HostID)
			if err := c.DecommissionVolume(appID, vol); err != nil {
				log.Error("decommission volume", "app", name, "volume", vol.ID, "err", err)
				continue
			}
			decommissioned++
		}
	}
	log.Info("stale volume reconciliation complete", "decommissioned", decommissioned)
	return nil
}

func (f *ClusterFixer) collectHostVolumeIDs() (map[string]struct{}, error) {
	ids := make(map[string]struct{})
	for _, h := range f.hosts {
		vols, err := h.ListVolumes()
		if err != nil {
			return nil, fmt.Errorf("list volumes on %s: %w", h.ID(), err)
		}
		for _, v := range vols {
			ids[v.ID] = struct{}{}
		}
	}
	return ids, nil
}

func shouldDecommissionStaleVolume(vol *ct.Volume, hostVolIDs map[string]struct{}) bool {
	if vol == nil || vol.DecommissionedAt != nil {
		return false
	}
	if _, onHost := hostVolIDs[vol.ID]; onHost {
		return false
	}
	return vol.Type == volume.VolumeTypeData
}

func isMissingVolumeError(msg string) bool {
	return strings.Contains(msg, "required volume") && strings.Contains(msg, "does not exist")
}
