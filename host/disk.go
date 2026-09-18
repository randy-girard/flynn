package main

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	host "github.com/flynn/flynn/host/types"
)

const (
	flynnDataRoot       = "/var/lib/flynn"
	diskWatchInterval   = 30 * time.Second
	diskFullCooldown    = 5 * time.Minute
	diskFullUsedPercent = 98
	diskFullMinFree     = 256 << 20 // 256 MiB
	diskFullMinTotal    = 1 << 30   // skip tiny filesystems (tmpfs, etc.)
)

// flynnNodeDiskPath is the filesystem Flynn itself lives on: the data root
// when that directory exists, otherwise the host root.
func flynnNodeDiskPath() string {
	if st, err := os.Stat(flynnDataRoot); err == nil && st.IsDir() {
		return flynnDataRoot
	}
	return "/"
}

// fillDiskStats records usage of the Flynn node filesystem into stats.
func fillDiskStats(stats *host.HostResourceStats) error {
	path := flynnNodeDiskPath()
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(path, &statfs); err != nil {
		if path != "/" {
			path = "/"
			if err2 := syscall.Statfs(path, &statfs); err2 != nil {
				return err2
			}
		} else {
			return err
		}
	}
	applyStatfs(stats, path, statfs)
	return nil
}

func applyStatfs(stats *host.HostResourceStats, path string, statfs syscall.Statfs_t) {
	bsize := uint64(statfs.Bsize)
	stats.DiskPath = path
	stats.DiskTotalBytes = statfs.Blocks * bsize
	// Bavail is what non-root writers (jobs) can still allocate.
	stats.DiskFreeBytes = statfs.Bavail * bsize
	usedBlocks := uint64(0)
	if statfs.Blocks > statfs.Bfree {
		usedBlocks = statfs.Blocks - statfs.Bfree
	}
	stats.DiskUsedBytes = usedBlocks * bsize
}

func diskUsedPercent(stats *host.HostResourceStats) int {
	if stats == nil || stats.DiskTotalBytes == 0 {
		return 0
	}
	return int(stats.DiskUsedBytes * 100 / stats.DiskTotalBytes)
}

// diskOutOfSpace is true when the Flynn node filesystem cannot accept more
// writes without risking job and image failures.
func diskOutOfSpace(stats *host.HostResourceStats) bool {
	if stats == nil || stats.DiskTotalBytes == 0 {
		return false
	}
	if stats.DiskFreeBytes == 0 {
		return true
	}
	if diskUsedPercent(stats) >= diskFullUsedPercent {
		return true
	}
	if stats.DiskTotalBytes >= diskFullMinTotal && stats.DiskFreeBytes < diskFullMinFree {
		return true
	}
	return false
}

func diskFullMetadata(stats *host.HostResourceStats) map[string]string {
	if stats == nil {
		return nil
	}
	return map[string]string{
		"disk_path":         stats.DiskPath,
		"disk_total_bytes":  strconv.FormatUint(stats.DiskTotalBytes, 10),
		"disk_used_bytes":   strconv.FormatUint(stats.DiskUsedBytes, 10),
		"disk_free_bytes":   strconv.FormatUint(stats.DiskFreeBytes, 10),
		"disk_used_percent": strconv.Itoa(diskUsedPercent(stats)),
	}
}

func isNoSpaceErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no space left") || strings.Contains(s, "enospc")
}
