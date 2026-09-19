package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/opencontainers/runc/libcontainer/configs"
	host "github.com/randy-girard/flynn/host/types"
	"golang.org/x/sys/unix"
)

func TestUseUserNS(t *testing.T) {
	if useUserNS(nil) {
		t.Fatal("nil job is not a user-ns candidate")
	}
	user := &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}}
	if !useUserNS(user) {
		t.Fatal("user jobs must run in a user namespace")
	}
	build := &host.Job{Metadata: map[string]string{"flynn-controller.type": "dockerbuilder"}}
	if useUserNS(build) {
		t.Fatal("build jobs keep the host user namespace for nested runc")
	}
	sys := &host.Job{Partition: "system", Metadata: map[string]string{"flynn-system-app": "true"}}
	if useUserNS(sys) {
		t.Fatal("system jobs must not remap UIDs")
	}
	data := &host.Job{Partition: "system", Metadata: map[string]string{"flynn-system-app": "true", "flynn-controller.app_name": "postgres"}}
	if useUserNS(data) {
		t.Fatal("datastore jobs must not remap UIDs")
	}
	// Official plugins install as flynn-system-app. Their appliance and UI
	// jobs keep host UID 0 so ZFS volumes and sirenia stay compatible.
	for _, name := range []string{
		"redis", "mariadb", "mongodb", "kafka", "clickhouse",
		"dashboard", "www", "discovery", "otel",
	} {
		plugin := &host.Job{
			Partition: "system",
			Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-plugin":              "true",
				"flynn-controller.app_name": name,
				"flynn-controller.type":     "web",
			},
		}
		if useUserNS(plugin) {
			t.Fatalf("plugin app %s must not remap UIDs", name)
		}
		ds := &host.Job{
			Partition: "system",
			Metadata: map[string]string{
				"flynn-system-app":          "true",
				"flynn-plugin":              "true",
				"flynn-controller.app_name": name,
				"flynn-controller.type":     name,
			},
		}
		if useUserNS(ds) {
			t.Fatalf("plugin datastore %s must not remap UIDs", name)
		}
	}
}

func TestSeedOverlayMountTargets(t *testing.T) {
	root := t.TempDir()
	srcFile := filepath.Join(t.TempDir(), "init")
	if err := os.WriteFile(srcFile, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), ".containerconfig")
	if err := os.WriteFile(cfg, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	mounts := []*configs.Mount{
		{Source: srcFile, Destination: "/.containerinit", Device: "bind"},
		{Source: cfg, Destination: "/.containerconfig", Device: "bind"},
		{Source: srcDir, Destination: "/.container-shared", Device: "bind"},
		{Source: "proc", Destination: "/proc", Device: "proc"},
		{Source: "tmpfs", Destination: "/dev", Device: "tmpfs"},
	}
	if err := seedOverlayMountTargets(root, mounts); err != nil {
		t.Fatal(err)
	}
	initInfo, err := os.Stat(filepath.Join(root, ".containerinit"))
	if err != nil || initInfo.IsDir() {
		t.Fatalf(".containerinit must be a file: %v", err)
	}
	cfgInfo, err := os.Stat(filepath.Join(root, ".containerconfig"))
	if err != nil || cfgInfo.IsDir() {
		t.Fatalf(".containerconfig must be a file: %v", err)
	}
	shared, err := os.Stat(filepath.Join(root, ".container-shared"))
	if err != nil || !shared.IsDir() {
		t.Fatalf(".container-shared dir: %v", err)
	}
	proc, err := os.Stat(filepath.Join(root, "proc"))
	if err != nil || !proc.IsDir() {
		t.Fatalf("/proc dir: %v", err)
	}
	dev, err := os.Stat(filepath.Join(root, "dev"))
	if err != nil || !dev.IsDir() {
		t.Fatalf("/dev must stay a directory: %v", err)
	}
}

func TestUserNSSpecForJobStableAndInRange(t *testing.T) {
	a := userNSSpecForJob("host-aaaa")
	b := userNSSpecForJob("host-aaaa")
	if a != b {
		t.Fatalf("same job id must map to the same range: %+v vs %+v", a, b)
	}
	if a.Size != userNSSize {
		t.Fatalf("size=%d want %d", a.Size, userNSSize)
	}
	if a.HostID < userNSHostBase {
		t.Fatalf("host uid %d is below base %d", a.HostID, userNSHostBase)
	}
	if a.HostID%userNSSize != userNSHostBase%userNSSize {
		t.Fatalf("host uid %d is not aligned to base %d size %d", a.HostID, userNSHostBase, userNSSize)
	}
	if a.HostID+a.Size-1 >= userNSMaxUID {
		t.Fatalf("range [%d,%d] overflows signed 32-bit uid", a.HostID, a.HostID+a.Size-1)
	}
	other := userNSSpecForJob("host-bbbb")
	if other.HostID == a.HostID {
		t.Logf("hash collision for test ids (ok, rare): both %d", a.HostID)
	}
}

func TestUserNSSpecCoversFlynnUID(t *testing.T) {
	s := userNSSpecForJob("job")
	host5000, ok := s.HostIDFor(5000)
	if !ok {
		t.Fatal("flynn uid 5000 must be inside the mapped range")
	}
	if host5000 != s.HostID+5000 {
		t.Fatalf("uid 5000 -> %d, want %d", host5000, s.HostID+5000)
	}
	if _, ok := s.HostIDFor(s.Size); ok {
		t.Fatal("container uid == size must be unmapped")
	}
	if _, ok := s.HostIDFor(-1); ok {
		t.Fatal("negative uid must be unmapped")
	}
	root, ok := s.HostIDFor(0)
	if !ok || root != s.HostID {
		t.Fatalf("container 0 -> %d ok=%v, want %d", root, ok, s.HostID)
	}
}

func TestApplyUserNSConfigPrependsNEWUSER(t *testing.T) {
	config := &configs.Config{
		Namespaces: configs.Namespaces{
			{Type: configs.NEWNS},
			{Type: configs.NEWPID},
			{Type: configs.NEWNET},
		},
	}
	spec := userNSSpec{HostID: 1_048_576, Size: 65536}
	applyUserNSConfig(config, spec)
	if !config.Namespaces.Contains(configs.NEWUSER) {
		t.Fatal("NEWUSER missing")
	}
	if config.Namespaces[0].Type != configs.NEWUSER {
		t.Fatalf("NEWUSER must be first, got %v", config.Namespaces)
	}
	if len(config.UidMappings) != 1 || config.UidMappings[0].HostID != spec.HostID || config.UidMappings[0].Size != spec.Size {
		t.Fatalf("uid maps=%v", config.UidMappings)
	}
	if len(config.GidMappings) != 1 || config.GidMappings[0].HostID != spec.HostID {
		t.Fatalf("gid maps=%v", config.GidMappings)
	}
	// idempotent
	applyUserNSConfig(config, spec)
	n := 0
	for _, ns := range config.Namespaces {
		if ns.Type == configs.NEWUSER {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("duplicate NEWUSER: %v", config.Namespaces)
	}
	root, err := config.HostUID(0)
	if err != nil || root != spec.HostID {
		t.Fatalf("HostUID(0)=%d err=%v want %d", root, err, spec.HostID)
	}
	flynnUID, err := config.HostUID(5000)
	if err != nil || flynnUID != spec.HostID+5000 {
		t.Fatalf("HostUID(5000)=%d err=%v want %d", flynnUID, err, spec.HostID+5000)
	}
}

func TestChownTree(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "sub", "f")
	if err := os.MkdirAll(filepath.Dir(nested), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	gid := os.Getgid()
	if err := chownTree(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(nested)
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)
	if int(sys.Uid) != uid || int(sys.Gid) != gid {
		t.Fatalf("uid/gid=%d/%d want %d/%d", sys.Uid, sys.Gid, uid, gid)
	}
}

func mountTmpfsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "flynn-idmap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Unmount(dir, unix.MNT_DETACH)
		_ = os.RemoveAll(dir)
	})
	if err := unix.Mount("tmpfs", dir, "tmpfs", 0, "mode=0755"); err != nil {
		t.Skipf("mount tmpfs: %v", err)
	}
	return dir
}

func TestPrepareUserNSMountsClonesVolumes(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to write uid_map and mount_setattr")
	}
	if _, err := os.Stat("/proc/self/ns/user"); err != nil {
		t.Skip("user namespaces not available")
	}
	vol := mountTmpfsDir(t)
	if err := os.WriteFile(filepath.Join(vol, "data"), []byte("v"), 0600); err != nil {
		t.Fatal(err)
	}
	dst, err := os.MkdirTemp("", "flynn-idmap-vol-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Unmount(dst, unix.MNT_DETACH)
		_ = os.RemoveAll(dst)
	})

	spec := userNSSpec{HostID: 1_000_000, Size: 65536}
	if err := prepareUserNSMounts([][2]string{{vol, dst}}, spec); err != nil {
		t.Fatal(err)
	}
	if mustStat(t, filepath.Join(vol, "data")).Uid != 0 {
		t.Fatalf("volume source must stay uid 0, got %d", mustStat(t, filepath.Join(vol, "data")).Uid)
	}
	if int(mustStat(t, filepath.Join(dst, "data")).Uid) != spec.HostID {
		t.Fatalf("volume clone uid=%d want %d", mustStat(t, filepath.Join(dst, "data")).Uid, spec.HostID)
	}
}

func TestUnmountUnderDeepestFirst(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(a, "b")
	if err := os.MkdirAll(b, 0755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", a, "tmpfs", 0, "mode=0755"); err != nil {
		t.Skipf("mount tmpfs: %v", err)
	}
	if err := os.MkdirAll(b, 0755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", b, "tmpfs", 0, "mode=0755"); err != nil {
		t.Fatal(err)
	}
	unmountUnder(base)
	if err := os.RemoveAll(base); err != nil {
		t.Fatalf("tree still busy after unmountUnder: %v", err)
	}
}

func TestMountUserNSWritable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	dir := t.TempDir()
	mnt, err := mountUserNSWritable(dir, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(mnt, unix.MNT_DETACH) })
	if mnt != filepath.Join(dir, "upperfs") {
		t.Fatalf("path=%s", mnt)
	}
	if err := os.WriteFile(filepath.Join(mnt, "f"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestChownJobTmpSkipsIdmapDirs(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "containerinit")
	skipDir := filepath.Join(dir, idmapLayerDir, "0")
	if err := os.WriteFile(keep, []byte("init"), 0500); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(skipDir, 0755); err != nil {
		t.Fatal(err)
	}
	skipFile := filepath.Join(skipDir, "f")
	if err := os.WriteFile(skipFile, []byte("layer"), 0644); err != nil {
		t.Fatal(err)
	}
	upperFile := filepath.Join(dir, "upperfs", "overlay-upperdir", "f")
	if err := os.MkdirAll(filepath.Dir(upperFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upperFile, []byte("upper"), 0644); err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	gid := os.Getgid()
	if uid == 0 {
		if err := os.Lchown(skipFile, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(upperFile, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := os.Lchown(keep, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := chownJobTmp(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	if mustStat(t, keep).Uid != uint32(uid) {
		t.Fatalf("containerinit uid=%d want %d", mustStat(t, keep).Uid, uid)
	}
	if uid == 0 && mustStat(t, skipFile).Uid != 1 {
		t.Fatalf("idmap layer was chowned to %d; must be skipped", mustStat(t, skipFile).Uid)
	}
	if uid == 0 && mustStat(t, upperFile).Uid != 1 {
		t.Fatalf("overlay upper was chowned to %d; must be skipped", mustStat(t, upperFile).Uid)
	}
}

func TestIDMapCloneMountIndependentOfSource(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	src, err := os.MkdirTemp("", "flynn-idmap-src-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(src)
	if err := unix.Mount("tmpfs", src, "tmpfs", 0, "mode=0755"); err != nil {
		t.Skipf("mount tmpfs: %v", err)
	}
	defer unix.Unmount(src, unix.MNT_DETACH)
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("v"), 0600); err != nil {
		t.Fatal(err)
	}

	dst, err := os.MkdirTemp("", "flynn-idmap-dst-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dst)

	spec := userNSSpec{HostID: 1_065_536, Size: 65536}
	userns, done, err := spawnMappedUserns(spec.sysProcMaps())
	if err != nil {
		t.Skipf("userns helper: %v", err)
	}
	defer done()
	if err := idmapCloneMount(src, dst, int(userns.Fd())); err != nil {
		t.Fatalf("idmap clone: %v", err)
	}
	defer unix.Unmount(dst, unix.MNT_DETACH)

	srcUID := int(mustStat(t, filepath.Join(src, "f")).Uid)
	dstUID := int(mustStat(t, filepath.Join(dst, "f")).Uid)
	if srcUID != 0 {
		t.Fatalf("source mount must stay unshifted, uid=%d", srcUID)
	}
	if dstUID != spec.HostID {
		t.Fatalf("cloned mount uid=%d want %d", dstUID, spec.HostID)
	}
}

func TestIDMapCloneSquashfsMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	if _, err := exec.LookPath("mksquashfs"); err != nil {
		t.Skip("mksquashfs not installed")
	}
	base, err := os.MkdirTemp("", "flynn-sq-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	src := filepath.Join(base, "src")
	mnt := filepath.Join(base, "mnt")
	img := filepath.Join(base, "layer.sqfs")
	if err := os.Mkdir(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mnt, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sh"), []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("mksquashfs", src, img, "-noappend", "-quiet").CombinedOutput()
	if err != nil {
		t.Skipf("mksquashfs: %v %s", err, out)
	}
	if err := exec.Command("mount", "-o", "loop,ro", img, mnt).Run(); err != nil {
		t.Skipf("mount squashfs: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(mnt, unix.MNT_DETACH) })

	dst := filepath.Join(base, "idmapped")
	if err := os.Mkdir(dst, 0755); err != nil {
		t.Fatal(err)
	}
	spec := userNSSpec{HostID: 1_179_648, Size: 65536}
	userns, done, err := spawnMappedUserns(spec.sysProcMaps())
	if err != nil {
		t.Skipf("userns helper: %v", err)
	}
	defer done()
	if err := idmapCloneMount(mnt, dst, int(userns.Fd())); err != nil {
		t.Fatalf("idmap clone squashfs (required for image layers): %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(dst, unix.MNT_DETACH) })
	if int(mustStat(t, filepath.Join(dst, "sh")).Uid) != spec.HostID {
		t.Fatalf("squashfs clone uid=%d want %d", mustStat(t, filepath.Join(dst, "sh")).Uid, spec.HostID)
	}
	if mustStat(t, filepath.Join(mnt, "sh")).Uid != 0 {
		t.Fatalf("shared squashfs uid=%d want 0", mustStat(t, filepath.Join(mnt, "sh")).Uid)
	}
}

func TestIDMapOverlayFromMappedLayers(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root")
	}
	base, err := os.MkdirTemp("", "flynn-ovl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	// Mapped-uid mkdir must be able to walk this path (MkdirTemp is 0700).
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	lower := filepath.Join(base, "l")
	tmpfs := filepath.Join(base, "t")
	merged := filepath.Join(base, "m")
	scratch := filepath.Join(base, "scratch")
	for _, d := range []string{lower, tmpfs, merged, scratch} {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mount("tmpfs", lower, "tmpfs", 0, "mode=0755"); err != nil {
		t.Skipf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(lower, unix.MNT_DETACH) })
	if err := unix.Mount("tmpfs", tmpfs, "tmpfs", 0, "mode=0755"); err != nil {
		t.Skipf("mount tmpfs: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(tmpfs, unix.MNT_DETACH) })
	if err := os.WriteFile(filepath.Join(lower, "sh"), []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		filepath.Join(tmpfs, "overlay-upperdir"),
		filepath.Join(tmpfs, "overlay-workdir"),
	} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	spec := userNSSpec{HostID: 1_114_112, Size: 65536}
	mapped, err := idmapCloneLayers(scratch, []string{lower}, spec)
	if err != nil {
		t.Fatalf("idmap layers: %v", err)
	}
	t.Cleanup(func() {
		for _, p := range mapped {
			_ = unix.Unmount(p, unix.MNT_DETACH)
		}
	})
	upper := filepath.Join(tmpfs, "overlay-upperdir")
	work := filepath.Join(tmpfs, "overlay-workdir")
	if err := os.Mkdir(filepath.Join(upper, "tmp"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(upper, "tmp"), os.ModeSticky|0777); err != nil {
		t.Fatal(err)
	}
	if err := chownTree(upper, spec.HostID, spec.HostID); err != nil {
		t.Fatal(err)
	}
	if err := chownTree(work, spec.HostID, spec.HostID); err != nil {
		t.Fatal(err)
	}
	opts := "lowerdir=" + mapped[0] + ",upperdir=" + upper + ",workdir=" + work
	if err := unix.Mount("overlay", merged, "overlay", 0, opts); err != nil {
		t.Fatalf("mount overlay on idmapped lower (required for user jobs): %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(merged, unix.MNT_DETACH) })
	if int(mustStat(t, filepath.Join(merged, "sh")).Uid) != spec.HostID {
		t.Fatalf("overlay uid=%d want %d", mustStat(t, filepath.Join(merged, "sh")).Uid, spec.HostID)
	}
	if mustStat(t, filepath.Join(lower, "sh")).Uid != 0 {
		t.Fatalf("original lower uid=%d want 0", mustStat(t, filepath.Join(lower, "sh")).Uid)
	}
	tmp := filepath.Join(merged, "tmp")
	if err := os.Chmod(tmp, os.ModeSticky|0777); err != nil {
		t.Fatalf("host chmod overlay /tmp (must stay writable): %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "f"), []byte("w"), 0644); err != nil {
		t.Fatalf("host write overlay /tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(merged, ".containerinit"), nil, 0755); err != nil {
		t.Fatalf("host create overlay bind target: %v", err)
	}
	if err := prepareUserNSOverlayRoot(merged, spec); err != nil {
		t.Fatalf("prepare overlay root: %v", err)
	}
	if err := chownTree(upper, spec.HostID, spec.HostID); err != nil {
		t.Fatal(err)
	}
	if int(mustStat(t, tmp).Uid) != spec.HostID {
		t.Fatalf("chowned overlay /tmp uid=%d want %d", mustStat(t, tmp).Uid, spec.HostID)
	}
	if int(mustStat(t, filepath.Join(merged, ".containerinit")).Uid) != spec.HostID {
		t.Fatalf("chowned .containerinit uid=%d want %d", mustStat(t, filepath.Join(merged, ".containerinit")).Uid, spec.HostID)
	}
	rootStat := mustStat(t, merged)
	if int(rootStat.Uid) != spec.HostID {
		t.Fatalf("overlay root uid=%d want %d", rootStat.Uid, spec.HostID)
	}
	for _, d := range []string{base, merged} {
		if err := os.Chmod(d, 0755); err != nil {
			t.Fatalf("chmod %s: %v", d, err)
		}
	}
	www := filepath.Join(merged, "www")
	cmd := exec.Command("mkdir", www)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  unix.CLONE_NEWUSER,
		UidMappings: spec.sysProcMaps(),
		GidMappings: spec.sysProcMaps(),
		Credential:  &syscall.Credential{Uid: 0, Gid: 0},
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mapped root mkdir overlay /www (docker images write here): %v (%s)", err, out)
	}
	if _, err := os.Stat(www); err != nil {
		t.Fatalf("overlay /www missing after mapped mkdir: %v", err)
	}
}

func TestUserJobHome(t *testing.T) {
	if got := userJobHome(false); got != "/" {
		t.Fatalf("system home=%q want /", got)
	}
	if got := userJobHome(true); got != "/tmp" {
		t.Fatalf("user-ns home=%q want /tmp", got)
	}
}

func TestPrepareUserNSOverlayRoot(t *testing.T) {
	dir := t.TempDir()
	spec := userNSSpec{HostID: os.Getuid(), Size: 65536}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := prepareUserNSOverlayRoot(dir, spec); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0755 {
		t.Fatalf("mode=%o want 0755", st.Mode().Perm())
	}
	if int(mustStat(t, dir).Uid) != spec.HostID {
		t.Fatalf("uid=%d want %d", mustStat(t, dir).Uid, spec.HostID)
	}
}

func mustStat(t *testing.T, path string) *syscall.Stat_t {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t)
}
