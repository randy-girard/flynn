package cleanup

import "syscall"

const (
	// MinFreeBeforeImagePull is the minimum free space required on the
	// filesystem that hosts layer-cache and per-job image materialization
	// before pulling update images.
	MinFreeBeforeImagePull = 5 << 30 // 5 GiB
)

// FSFreeBytes returns the available bytes for unprivileged users on the
// filesystem containing path.
func FSFreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}
