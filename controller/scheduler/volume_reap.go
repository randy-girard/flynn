package main

import (
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// decommissionedVolumeGrace is how long a decommissioned volume stays on its
// host before the scheduler destroys it. Deleting an app decommissions its
// volumes (NEW-35); so do a redis failover and a review app cleanup. The
// grace covers jobs that are still stopping and leaves an operator a short
// window to copy data off the host.
const decommissionedVolumeGrace = 10 * time.Minute

// reapDecommissionedVolumes destroys, on their hosts, decommissioned volumes
// whose grace period has passed and that no live job holds, then records
// them destroyed. Only the leader does it, and only on healthy hosts. A host
// that no longer has the volume answers 404, which counts as destroyed, so
// the destroy is idempotent. A failed destroy (a busy dataset, a host that
// went away) is retried on the next pass. It runs on the scheduler loop
// after each volume sync.
func (s *Scheduler) reapDecommissionedVolumes(now time.Time) {
	if s.isLeader == nil || !*s.isLeader {
		return
	}
	log := s.logger.New("fn", "reapDecommissionedVolumes")
	held := s.volumesHeldByLiveJobs()
	if s.volumeReaping == nil {
		s.volumeReaping = make(map[string]struct{})
	}
	for id, vol := range s.volumes {
		if vol.DecommissionedAt == nil || vol.GetState() == ct.VolumeStateDestroyed || vol.HostID == "" {
			continue
		}
		if now.Sub(*vol.DecommissionedAt) < decommissionedVolumeGrace {
			continue
		}
		if _, ok := s.volumeReaping[id]; ok {
			continue
		}
		if _, ok := held[id]; ok || s.volumeHeldByLiveJob(vol) {
			continue
		}
		h, ok := s.hosts[vol.HostID]
		if !ok || !h.Healthy || h.client == nil {
			continue
		}
		s.volumeReaping[id] = struct{}{}
		destroy := func(vol *Volume, h *Host) {
			err := h.client.DestroyVolume(vol.ID)
			gone := err == nil || err == cluster.ErrNotFound || httphelper.IsObjectNotFoundError(err)
			s.onLoop(func() {
				delete(s.volumeReaping, vol.ID)
				if !gone {
					log.Warn("could not destroy decommissioned volume, will retry", "vol.id", vol.ID, "host.id", vol.HostID, "app.id", vol.AppID, "err", err)
					return
				}
				log.Info("destroyed decommissioned volume", "vol.id", vol.ID, "host.id", vol.HostID, "app.id", vol.AppID, "job.type", vol.JobType, "decommissioned_at", vol.DecommissionedAt, "already_gone", err != nil)
				vol.SetState(ct.VolumeStateDestroyed)
				vol.JobID = nil
				s.persistVolume(vol)
			})
		}
		if !s.loopActive {
			// tests and startup: no loop to hand the result back to
			destroy(vol, h)
			continue
		}
		s.volumeReaps.Add(1)
		go func(vol *Volume, h *Host) {
			defer s.volumeReaps.Done()
			destroy(vol, h)
		}(vol, h)
	}
}

// volumesHeldByLiveJobs returns the IDs of volumes a job that has not
// stopped lists, so a volume whose JobID was cleared but that a pending or
// running job still names is not destroyed under it.
func (s *Scheduler) volumesHeldByLiveJobs() map[string]struct{} {
	out := make(map[string]struct{})
	for _, job := range s.jobs {
		if job == nil || job.State == JobStateStopped {
			continue
		}
		for _, v := range job.Volumes {
			if v != nil {
				out[v.ID] = struct{}{}
			}
		}
	}
	return out
}
