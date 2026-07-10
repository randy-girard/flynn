package cleanup

import (
	"os"
	"path/filepath"
	"strings"

	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/host/volume"
)

const (
	ImageTmpDir    = "/var/lib/flynn/image/tmp"
	ImageMntDir    = "/var/lib/flynn/image/mnt"
	LayerCacheDir  = "/var/lib/flynn/layer-cache"
	LayerCacheSuff = ".squashfs"
)

// ImageData removes orphaned per-job image material and unreferenced layer-cache
// files on the local host filesystem. hostID must be this machine's host ID.
// Only image directories for jobs owned by hostID are considered for removal.
func ImageData(hostID string, jobs map[string]host.ActiveJob, vols []*volume.Info) error {
	keepJobs := keepJobIDs(jobs)
	keepLayers := keepLayerIDs(jobs, vols)

	for _, base := range []string{ImageTmpDir, ImageMntDir} {
		entries, err := os.ReadDir(base)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, name := range orphanImageDirs(entries, keepJobs, hostID) {
			path := filepath.Join(base, name)
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}

	cacheEntries, err := os.ReadDir(LayerCacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, id := range unreferencedLayerIDs(cacheEntries, keepLayers) {
		_ = os.Remove(filepath.Join(LayerCacheDir, id+LayerCacheSuff))
		_ = os.Remove(filepath.Join(LayerCacheDir, id+".json"))
	}
	return nil
}

func keepJobIDs(jobs map[string]host.ActiveJob) map[string]struct{} {
	keep := make(map[string]struct{}, len(jobs))
	for id, j := range jobs {
		if j.Status == host.StatusRunning || j.Status == host.StatusStarting {
			keep[id] = struct{}{}
		}
	}
	return keep
}

func keepLayerIDs(jobs map[string]host.ActiveJob, vols []*volume.Info) map[string]struct{} {
	keep := make(map[string]struct{})
	for _, v := range vols {
		if v.Type == volume.VolumeTypeSquashfs {
			keep[v.ID] = struct{}{}
		}
	}
	for _, j := range jobs {
		if j.Job == nil {
			continue
		}
		if j.Status == host.StatusDone {
			continue
		}
		for _, m := range j.Job.Mountspecs {
			if m.Type == host.MountspecTypeSquashfs && m.ID != "" {
				keep[m.ID] = struct{}{}
			}
		}
	}
	return keep
}

func orphanImageDirs(entries []os.DirEntry, keep map[string]struct{}, hostID string) []string {
	var orphans []string
	hostPrefix := hostID + "-"
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), hostPrefix) {
			continue
		}
		if _, ok := keep[e.Name()]; ok {
			continue
		}
		orphans = append(orphans, e.Name())
	}
	return orphans
}

func unreferencedLayerIDs(entries []os.DirEntry, keep map[string]struct{}) []string {
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, LayerCacheSuff) {
			continue
		}
		id := strings.TrimSuffix(name, LayerCacheSuff)
		if _, ok := keep[id]; ok {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
