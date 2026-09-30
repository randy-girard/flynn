package zfs

import (
	"strings"

	log "github.com/inconshreveable/log15"
	gzfs "github.com/mistifyio/go-zfs"
)

type Logger struct {
	logger log.Logger
}

func (l *Logger) Log(msg []string) {
	l.logger.Debug(strings.Join(msg, " "))
}

func init() {
	logger := Logger{
		logger: log.New(log.Ctx{"package": "zfs"}),
	}
	gzfs.SetLogger(&logger)
}

func isDatasetNotExistsError(e error) bool {
	if e == nil {
		return false
	}
	return strings.Contains(e.Error(), "dataset does not exist")
}

/*
"dataset is busy" errors from ZFS typically indicate that there are open
files in that dataset mount. go-zfs sometimes omits the trailing newline.
*/
func IsDatasetBusyError(e error) bool {
	if e == nil {
		return false
	}
	return strings.Contains(e.Error(), "dataset is busy")
}

/*
"has children" errors from ZFS occur when removing a volume that has
snapshots.  ZFS requires snapshots of a volume to be deleted first.
*/
func IsDatasetHasChildrenError(e error) bool {
	lines := strings.SplitN(e.Error(), "\n", 2)
	return strings.HasSuffix(lines[0], "has children")
}
