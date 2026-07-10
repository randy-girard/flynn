package fixer

import (
	"time"

	"github.com/flynn/flynn/pkg/cluster"
)

// FixLocalDisk removes orphaned image layer material on each host by invoking
// cleanup on that host's flynn-host daemon. Disk paths are always local to the
// host that owns them; never delete image data from the machine running fix.
func (f *ClusterFixer) FixLocalDisk() error {
	const cleanupTimeout = 2 * time.Minute
	for _, h := range f.hosts {
		log := f.l.New("fn", "FixLocalDisk", "host", h.ID())
		log.Info("cleaning orphaned image data")
		done := make(chan error, 1)
		go func(host *cluster.Host) {
			done <- host.CleanupImageData()
		}(h)
		select {
		case err := <-done:
			if err != nil {
				log.Error("local disk cleanup failed", "err", err)
			}
		case <-time.After(cleanupTimeout):
			log.Error("local disk cleanup timed out")
		}
	}
	return nil
}

// cleanupImageDataOnHost runs image cleanup on a single host.
func cleanupImageDataOnHost(h *cluster.Host) error {
	return h.CleanupImageData()
}
