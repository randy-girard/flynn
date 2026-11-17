package zfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func funcBody(src, sig string) string {
	start := strings.Index(src, sig)
	if start < 0 {
		return ""
	}
	rest := src[start:]
	if i := strings.Index(rest[len(sig):], "\nfunc "); i >= 0 {
		return rest[:len(sig)+i]
	}
	return rest
}

func TestDestroyUnmountUsesHelper(t *testing.T) {
	src, err := os.ReadFile("zfs.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	destroy := funcBody(body, "func (p *Provider) destroy(")
	if destroy == "" {
		t.Fatal("missing Provider.destroy")
	}
	if !strings.Contains(destroy, "unmountIfMounted(") {
		t.Fatal("destroy must call unmountIfMounted")
	}
	if strings.Contains(destroy, "syscall.Unmount(vol.basemount, 0)") {
		t.Fatal("destroy must not return err straight from syscall.Unmount(vol.basemount, 0)")
	}
	if !strings.Contains(destroy, "p.mountDataset(vol)") {
		t.Fatal("failed zfs destroy must still remount")
	}

	helper := funcBody(body, "func unmountIfMounted(")
	if helper == "" {
		t.Fatal("missing unmountIfMounted")
	}
	if !strings.Contains(helper, "isMount(") {
		t.Fatal("unmountIfMounted must unmount only when isMount says the path is mounted")
	}
	if !strings.Contains(helper, "syscall.EINVAL") || !strings.Contains(helper, "syscall.ENOENT") {
		t.Fatal("unmountIfMounted must treat EINVAL and ENOENT as not mounted")
	}
	if !strings.Contains(helper, "return err") {
		t.Fatal("a busy unmount must still fail")
	}
}

func TestUnmountIfMounted(t *testing.T) {
	dir := t.TempDir()
	if err := unmountIfMounted(dir); err != nil {
		t.Fatalf("plain temp directory: %v", err)
	}
	if err := unmountIfMounted(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing path: %v", err)
	}
}
