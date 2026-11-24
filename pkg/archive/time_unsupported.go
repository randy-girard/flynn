//go:build !linux && !windows
// +build !linux,!windows

package archive

// utimeOmit is UTIME_OMIT from <sys/stat.h> on macOS and the BSDs.
const utimeOmit = -2
