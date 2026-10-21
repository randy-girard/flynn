package main

import (
	"path/filepath"
	"strings"
	"syscall"

	"github.com/randy-girard/flynn/host/orphan"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// reapOrphanContainers drops containerinit processes that survived a host
// restart without a workload. They hold ext2 zvols open, and ZFS then refuses
// to destroy those volumes with "dataset is busy".
func (h *Host) reapOrphanContainers() {
	if h == nil {
		return
	}
	killed, inUse, err := orphan.ReapAll(orphan.ProcRoot, h.trackedContainerPIDs(), orphan.Kill)
	if err != nil {
		h.log.Warn("could not scan orphaned containerinit processes", "err", err)
		return
	}
	if len(killed) == 0 && len(inUse) == 0 {
		return
	}
	h.log.Info("orphaned containerinit processes", "reaped", len(killed), "still_in_use", len(inUse))
}

// prepareVolumeDestroy releases a leftover containerinit for this ext2 volume
// before ZFS destroy. A container that still has a workload is left mounted.
func (h *Host) prepareVolumeDestroy(id string) error {
	if h == nil {
		return nil
	}
	// The job overlay is mounted on the host, outside the container. Killing
	// an orphaned containerinit does not drop it, and ZFS then refuses to
	// destroy the ext2 zvol underneath.
	unmountJobDisk(id)
	_, err := orphan.ReapJob(orphan.ProcRoot, id, h.trackedContainerPIDs(), orphan.Kill)
	if err == nil {
		return nil
	}
	if inUse, ok := err.(*orphan.InUseError); ok {
		return httphelper.JSONError{Code: httphelper.ConflictErrorCode, Message: inUse.Error()}
	}
	h.log.Warn("could not release orphaned container for volume", "volume.id", id, "err", err)
	return nil
}

func (h *Host) trackedContainerPIDs() map[int]struct{} {
	out := map[int]struct{}{}
	if h == nil || h.state == nil {
		return out
	}
	for _, job := range h.state.Get() {
		if pid := containerPID(job); pid > 1 {
			out[pid] = struct{}{}
		}
	}
	return out
}

func unmountJobDisk(jobID string) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || strings.Contains(jobID, "/") || strings.Contains(jobID, "..") {
		return
	}
	for _, path := range []string{
		filepath.Join("/var/lib/flynn/image/mnt", jobID),
		filepath.Join("/var/lib/flynn/image/tmp", jobID),
		filepath.Join("/var/lib/flynn/volumes/zfs/mnt/ext2", jobID),
	} {
		_ = syscall.Unmount(path, syscall.MNT_DETACH)
	}
}

func containerPID(job *host.ActiveJob) int {
	if job == nil || job.PID == nil {
		return 0
	}
	return *job.PID
}
