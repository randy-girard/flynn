package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/opencontainers/runc/libcontainer/configs"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/netpolicy"
	"golang.org/x/sys/unix"
)

// User jobs run in a user namespace so container 0 is an unprivileged host
// UID. Overlay squashfs/ext2 layers stay owned by host 0/5000 on disk;
// idmapped mounts make those files appear as 0/5000 inside the job.
const (
	userNSHostBase = 1_000_000
	userNSSize     = 65536
	// Keep mapped UIDs inside signed 32-bit so overlay/ZFS and older
	// userland do not treat them as overflow.
	userNSMaxUID  = 1<<31 - 1
	idmapLayerDir = "idmap-layer"
	idmapVolDir   = "idmap-vol"
)

type userNSSpec struct {
	HostID int
	Size   int
}

func useUserNS(job *host.Job) bool {
	if job == nil {
		return false
	}
	return netpolicy.ClassifyJob(job) == netpolicy.ClassUser
}

func userNSSpecForJob(jobID string) userNSSpec {
	h := fnv.New32a()
	_, _ = h.Write([]byte(jobID))
	slots := uint32((userNSMaxUID - userNSSize - userNSHostBase) / userNSSize)
	if slots == 0 {
		slots = 1
	}
	slot := h.Sum32() % slots
	return userNSSpec{
		HostID: userNSHostBase + int(slot)*userNSSize,
		Size:   userNSSize,
	}
}

func (s userNSSpec) Maps() []configs.IDMap {
	return []configs.IDMap{{
		ContainerID: 0,
		HostID:      s.HostID,
		Size:        s.Size,
	}}
}

func (s userNSSpec) sysProcMaps() []syscall.SysProcIDMap {
	return []syscall.SysProcIDMap{{
		ContainerID: 0,
		HostID:      s.HostID,
		Size:        s.Size,
	}}
}

func (s userNSSpec) HostIDFor(containerID int) (int, bool) {
	if containerID < 0 || containerID >= s.Size {
		return -1, false
	}
	return s.HostID + containerID, true
}

func applyUserNSConfig(config *configs.Config, spec userNSSpec) {
	if config.Namespaces.Contains(configs.NEWUSER) {
		config.Namespaces.Remove(configs.NEWUSER)
	}
	config.Namespaces = append(configs.Namespaces{{Type: configs.NEWUSER}}, config.Namespaces...)
	maps := spec.Maps()
	config.UidMappings = maps
	config.GidMappings = maps
}

func chownTree(root string, uid, gid int) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

func overlayMountTarget(rootfs, dest string) string {
	rel := strings.TrimPrefix(filepath.Clean("/"+dest), "/")
	if rel == "" || rel == "." {
		return rootfs
	}
	return filepath.Join(rootfs, rel)
}

// prepareUserNSOverlayRoot makes overlay `/` writable by the mapped root.
// Overlay permission checks on an idmapped squashfs lower still use the
// unmapped image UID, so mkdir at `/` fails inside NEWUSER unless `/`
// is chowned through the overlay mount (upperdir chown after mount is
// not enough — overlay caches the root inode at mount time).
func prepareUserNSOverlayRoot(rootfs string, spec userNSSpec) error {
	if err := os.Chmod(rootfs, 0777); err != nil {
		return fmt.Errorf("chmod overlay root: %w", err)
	}
	if err := os.Lchown(rootfs, spec.HostID, spec.HostID); err != nil {
		return fmt.Errorf("chown overlay root: %w", err)
	}
	if err := os.Chmod(rootfs, 0755); err != nil {
		return fmt.Errorf("restore overlay root mode: %w", err)
	}
	return nil
}

func userJobHome(userNS bool) string {
	if userNS {
		return "/tmp"
	}
	return "/"
}

// seedOverlayMountTargets creates bind-mount destinations in the overlay as
// host 0 so runc does not have to create them after NEWUSER (EACCES on
// idmapped lowers / unmapped upper). Call before chowning the upper dir.
func seedOverlayMountTargets(rootfs string, mounts []*configs.Mount) error {
	for _, m := range mounts {
		if m == nil || m.Destination == "" {
			continue
		}
		dest := overlayMountTarget(rootfs, m.Destination)
		if dest == rootfs {
			continue
		}
		isFile := false
		if m.Device == "bind" || m.Device == "" {
			if m.Source != "" {
				if st, err := os.Stat(m.Source); err == nil {
					isFile = !st.IsDir()
				}
			}
		}
		if isFile {
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_RDWR, 0755)
			if err != nil {
				return fmt.Errorf("seed mount target %s: %w", dest, err)
			}
			_ = f.Close()
			continue
		}
		if err := os.MkdirAll(dest, 0755); err != nil {
			return fmt.Errorf("seed mount target %s: %w", dest, err)
		}
	}
	return nil
}

func spawnMappedUserns(maps []syscall.SysProcIDMap) (*os.File, func(), error) {
	start := func(bin string, args ...string) (*exec.Cmd, error) {
		cmd := exec.Command(bin, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  unix.CLONE_NEWUSER,
			UidMappings: maps,
			GidMappings: maps,
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
	cmd, err := start("/bin/sleep", "infinity")
	if err != nil {
		cmd, err = start("/bin/sleep", "3600")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("user namespace helper: %w", err)
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/ns/user", cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, nil, err
	}
	cleanup := func() {
		_ = f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	return f, cleanup, nil
}

func idmapCloneMount(source, target string, usernsFD int) error {
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	fd, err := unix.OpenTree(unix.AT_FDCWD, source, uint(unix.OPEN_TREE_CLONE|unix.O_CLOEXEC|unix.AT_RECURSIVE))
	if err != nil {
		return fmt.Errorf("open_tree %s: %w", source, err)
	}
	defer unix.Close(fd)
	attr := &unix.MountAttr{
		Attr_set:  unix.MOUNT_ATTR_IDMAP,
		Userns_fd: uint64(usernsFD),
	}
	if err := unix.MountSetattr(fd, "", unix.AT_EMPTY_PATH|unix.AT_RECURSIVE, attr); err != nil {
		return fmt.Errorf("idmap clone %s: %w", source, err)
	}
	if err := unix.MoveMount(fd, "", unix.AT_FDCWD, target, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		return fmt.Errorf("move_mount %s -> %s: %w", source, target, err)
	}
	return nil
}

func unmountUnder(root string) {
	if root == "" {
		return
	}
	root = filepath.Clean(root)
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

func mountUserNSWritable(scratch string, size int64) (string, error) {
	if size <= 0 {
		size = 1 << 30
	}
	dir := filepath.Join(scratch, "upperfs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	opts := fmt.Sprintf("size=%d,mode=0755", size)
	if err := unix.Mount("tmpfs", dir, "tmpfs", 0, opts); err != nil {
		return "", fmt.Errorf("mount writable tmpfs: %w", err)
	}
	return dir, nil
}

func idmapCloneLayers(scratch string, layers []string, spec userNSSpec) ([]string, error) {
	userns, done, err := spawnMappedUserns(spec.sysProcMaps())
	if err != nil {
		return nil, err
	}
	defer done()
	fd := int(userns.Fd())
	out := make([]string, len(layers))
	for i, layer := range layers {
		dst := filepath.Join(scratch, idmapLayerDir, strconv.Itoa(i))
		if err := idmapCloneMount(layer, dst, fd); err != nil {
			return nil, fmt.Errorf("idmap layer %s: %w", layer, err)
		}
		out[i] = dst
	}
	runtime.KeepAlive(userns)
	return out, nil
}

func chownJobTmp(tmpPath string, uid, gid int) error {
	return filepath.Walk(tmpPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == tmpPath {
			return os.Lchown(path, uid, gid)
		}
		rel, err := filepath.Rel(tmpPath, path)
		if err != nil {
			return err
		}
		first, _, _ := strings.Cut(rel, string(os.PathSeparator))
		if first == idmapLayerDir || first == idmapVolDir || first == "upperfs" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return os.Lchown(path, uid, gid)
	})
}

// prepareUserNSMounts clones job volumes with an idmapped mount. Overlay
// rootfs idmaps squashfs lowers only; the tmpfs upper stays unmapped so
// overlay copy-up works. The merged overlay cannot be idmapped (EINVAL).
func prepareUserNSMounts(volumes [][2]string, spec userNSSpec) error {
	if len(volumes) == 0 {
		return nil
	}
	userns, done, err := spawnMappedUserns(spec.sysProcMaps())
	if err != nil {
		return err
	}
	defer done()
	fd := int(userns.Fd())
	for _, pair := range volumes {
		if err := idmapCloneMount(pair[0], pair[1], fd); err != nil {
			return fmt.Errorf("idmap volume %s: %w", pair[0], err)
		}
	}
	runtime.KeepAlive(userns)
	return nil
}
