package main

import (
	"errors"
	"strconv"
	"strings"
	"syscall"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

const (
	hostRootFS          = "/"
	diskWatchInterval   = 30 * time.Second
	diskFullCooldown    = 5 * time.Minute
	diskFullUsedPercent = 98
	diskFullMinFree     = 256 << 20 // 256 MiB
	diskFullMinTotal    = 1 << 30   // skip tiny filesystems (tmpfs, etc.)
)

// fillDiskStats records usage of the host root filesystem into stats.
// CPU and memory are already machine-wide; disk matches that instead of
// only the Flynn data directory.
func fillDiskStats(stats *host.HostResourceStats) error {
	var statfs syscall.Statfs_t
	if err := syscall.Statfs(hostRootFS, &statfs); err != nil {
		return err
	}
	applyStatfs(stats, hostRootFS, statfs)
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

// diskOutOfSpace is true when the host root filesystem cannot accept more
// writes without risking jobs, images, and the rest of the machine.
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
