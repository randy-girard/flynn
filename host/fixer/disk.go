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
	keepJobs := make(map[string]struct{}, len(activeJobs))
	for id, j := range activeJobs {
		if j.Status == host.StatusRunning || j.Status == host.StatusStarting {
			keepJobs[id] = struct{}{}
		}
	}

	for _, base := range []string{imageTmpDir, imageMntDir} {
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			id := e.Name()
			if _, ok := keepJobs[id]; ok {
				continue
			}
			path := filepath.Join(base, id)
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
	for _, e := range cacheEntries {
		name := e.Name()
		if !strings.HasSuffix(name, layerCacheSuff) {
			continue
		}
		id := strings.TrimSuffix(name, layerCacheSuff)
		if _, ok := keepVolIDs[id]; ok {
			continue
		}
		squashPath := filepath.Join(layerCacheDir, name)
		metaPath := filepath.Join(layerCacheDir, id+".json")
		log.Info("removing unreferenced layer-cache file", "path", squashPath)
		_ = os.Remove(squashPath)
		_ = os.Remove(metaPath)
	}
	return nil
}
