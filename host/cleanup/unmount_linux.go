package cleanup

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

func unmountTree(root string) {
	root = filepath.Clean(root)
	if root == "" || root == "/" {
		return
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		_ = syscall.Unmount(root, syscall.MNT_DETACH)
		return
	}
	var mps []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mp := fields[4]
		if mp == root || strings.HasPrefix(mp, root+"/") {
			mps = append(mps, mp)
		}
	}
	sort.Slice(mps, func(i, j int) bool { return len(mps[i]) > len(mps[j]) })
	for _, mp := range mps {
		_ = syscall.Unmount(mp, syscall.MNT_DETACH)
	}
}
