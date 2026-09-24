package cleanup

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// trimZpoolCmd runs `zpool trim -w` so a file-backed vdev can punch holes and
// return unused extents to the host filesystem. Tests replace this.
var trimZpoolCmd = func(pool string) ([]byte, error) {
	cmd := exec.Command("zpool", "trim", "-w", pool)
	return cmd.CombinedOutput()
}

// TrimZpool waits for ZFS TRIM on pool. Missing pools and vdevs that do not
// support TRIM are skipped so reclaim stays safe on mock backends and on
// dedicated disks that reject the operation.
func TrimZpool(pool string) error {
	pool = strings.TrimSpace(pool)
	if pool == "" {
		return nil
	}
	out, err := trimZpoolCmd(pool)
	if err == nil {
		return nil
	}
	if trimSkipped(string(out), err) {
		return nil
	}
	msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
	if msg == "" {
		return fmt.Errorf("zpool trim %s: %w", pool, err)
	}
	return fmt.Errorf("zpool trim %s: %w: %s", pool, err, msg)
}

func trimSkipped(out string, err error) bool {
	s := strings.ToLower(out)
	if err != nil {
		s += " " + strings.ToLower(err.Error())
	}
	for _, needle := range []string{
		"not supported",
		"does not support",
		"no such pool",
		"does not exist",
		"cannot open",
		"executable file not found",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
