package updaterdeploy

import (
	"fmt"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// Overridable for tests so the double ListVolumes check finishes quickly.
var staleVolumeRecheckDelay = 10 * time.Second

// RepairStaleVolumes marks controller volume records destroyed when the
// underlying dataset no longer exists on the assigned host. This can happen
// after manual volume GC or host cleanup while the scheduler still tracks the
// volume, which otherwise causes sirenia rolling deploys to hang until timeout
// with "required volume ... does not exist" on the host.
//
// A volume is only destroyed when it is missing from two consecutive
// ListVolumes polls, JobList succeeded (so the live-job guard can run),
// and the volume is not referenced by a live (up/starting) job. A briefly
// incomplete volume listing or a controller JobList blip after host restart
// must not wipe a sirenia data volume.
func RepairStaleVolumes(ctrl controller.Client, hosts []*cluster.Host, log log15.Logger) error {
	if log == nil {
		log = log15.New()
	}

	volumes, err := ctrl.VolumeList()
	if err != nil {
		return fmt.Errorf("list controller volumes: %w", err)
	}

	liveJobs, err := liveJobIDs(ctrl, volumes)
	if err != nil {
		log.Warn("could not list jobs while repairing volumes; not destroying volumes without live-job guard", "err", err)
		liveJobs = nil
	}

	first := hostVolumeIndex(hosts, log)
	if staleVolumeRecheckDelay > 0 {
		time.Sleep(staleVolumeRecheckDelay)
	}
	second := hostVolumeIndex(hosts, log)

	var repaired int
	for _, vol := range volumes {
		if volumeHeldByLiveJob(vol, liveJobs) {
			log.Warn("skipping stale volume destroy; volume still referenced by live job",
				"vol.id", vol.ID, "host.id", vol.HostID, "job.id", *vol.JobID)
			continue
		}
		if !shouldDestroyStaleVolume(vol, first, second, liveJobs) {
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

// shouldDestroyStaleVolume reports whether a controller volume record should
// be marked destroyed. A volume is destroyed only when it is missing from two
// consecutive host listings and is not referenced by a live job. A nil
// liveJobs map means JobList failed: do not destroy, because the live-job
// guard cannot run.
func shouldDestroyStaleVolume(vol *ct.Volume, first, second map[string]map[string]struct{}, liveJobs map[string]bool) bool {
	if vol == nil || vol.ID == "" || vol.HostID == "" {
		return false
	}
	if vol.State == ct.VolumeStateDestroyed || vol.DecommissionedAt != nil {
		return false
	}
	if liveJobs == nil {
		return false
	}
	if volumeHeldByLiveJob(vol, liveJobs) {
		return false
	}
	firstVols, ok1 := first[vol.HostID]
	secondVols, ok2 := second[vol.HostID]
	if !ok1 || !ok2 {
		return false
	}
	_, inFirst := firstVols[vol.ID]
	_, inSecond := secondVols[vol.ID]
	return !inFirst && !inSecond
}

func volumeHeldByLiveJob(vol *ct.Volume, liveJobs map[string]bool) bool {
	if vol == nil || vol.JobID == nil || liveJobs == nil {
		return false
	}
	return liveJobs[*vol.JobID]
}

func liveJobIDs(ctrl controller.Client, volumes []*ct.Volume) (map[string]bool, error) {
	appIDs := make(map[string]struct{})
	for _, vol := range volumes {
		if vol != nil && vol.AppID != "" {
			appIDs[vol.AppID] = struct{}{}
		}
	}
	live := make(map[string]bool)
	for appID := range appIDs {
		jobs, err := ctrl.JobList(appID)
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if job == nil {
				continue
			}
			if job.State == ct.JobStateUp || job.State == ct.JobStateStarting {
				live[job.ID] = true
			}
		}
	}
	return live, nil
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
