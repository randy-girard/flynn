package fixer

import (
	"os"
	"path/filepath"
	"strings"

	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/cluster"
	"github.com/inconshreveable/log15"
)

const (
	imageTmpDir    = "/var/lib/flynn/image/tmp"
	imageMntDir    = "/var/lib/flynn/image/mnt"
	layerCacheDir  = "/var/lib/flynn/layer-cache"
	layerCacheSuff = ".squashfs"
)

// FixLocalDisk removes orphaned image layer material on each host. It never
// deletes controller data volumes or runs host volume GC.
func (f *ClusterFixer) FixLocalDisk() error {
	for _, h := range f.hosts {
		log := f.l.New("fn", "FixLocalDisk", "host", h.ID())
		if err := f.cleanupHostDisk(h, log); err != nil {
			log.Error("local disk cleanup failed", "err", err)
		}
	}
	return nil
}

func (f *ClusterFixer) cleanupHostDisk(h *cluster.Host, log log15.Logger) error {
	log.Info("cleaning orphaned image data")

	activeJobs, err := h.ListJobs()
	if err != nil {
		return err
	}
	keepJobs := keepJobIDs(activeJobs)

	for _, base := range []string{imageTmpDir, imageMntDir} {
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, name := range orphanImageDirs(entries, keepJobs) {
			path := filepath.Join(base, name)
			log.Info("removing orphaned image dir", "path", path)
			if err := os.RemoveAll(path); err != nil {
				log.Info("failed to remove orphaned image dir", "path", path, "err", err)
			}
		}
	}

	vols, err := h.ListVolumes()
	if err != nil {
		return err
	}
	keepVolIDs := make(map[string]struct{}, len(vols))
	for _, v := range vols {
		keepVolIDs[v.ID] = struct{}{}
	}

	cacheEntries, err := os.ReadDir(layerCacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, id := range unreferencedLayerIDs(cacheEntries, keepVolIDs) {
		squashPath := filepath.Join(layerCacheDir, id+layerCacheSuff)
		metaPath := filepath.Join(layerCacheDir, id+".json")
		log.Info("removing unreferenced layer-cache file", "path", squashPath)
		_ = os.Remove(squashPath)
		_ = os.Remove(metaPath)
	}
	return nil
}

// keepJobIDs returns the set of job IDs that are running or starting and whose
// on-disk image material must be preserved.
func keepJobIDs(jobs map[string]host.ActiveJob) map[string]struct{} {
	keep := make(map[string]struct{}, len(jobs))
	for id, j := range jobs {
		if j.Status == host.StatusRunning || j.Status == host.StatusStarting {
			keep[id] = struct{}{}
		}
	}
	return keep
}

// orphanImageDirs returns the names of directory entries (job IDs) that are not
// backed by a kept job and can be removed. Non-directory entries are ignored.
func orphanImageDirs(entries []os.DirEntry, keep map[string]struct{}) []string {
	var orphans []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := keep[e.Name()]; ok {
			continue
		}
		orphans = append(orphans, e.Name())
	}
	return orphans
}

// unreferencedLayerIDs returns the layer IDs of .squashfs cache files that are
// not referenced by any kept volume ID and can be removed.
func unreferencedLayerIDs(entries []os.DirEntry, keepVolIDs map[string]struct{}) []string {
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, layerCacheSuff) {
			continue
		}
		id := strings.TrimSuffix(name, layerCacheSuff)
		if _, ok := keepVolIDs[id]; ok {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
