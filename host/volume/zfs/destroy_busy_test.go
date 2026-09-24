package zfs

import (
	"os"
	"strings"
	"testing"
)

func TestDestroyRemountsOnFailure(t *testing.T) {
	src, err := os.ReadFile("zfs.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	destroy := strings.Index(body, "func (p *Provider) destroy(")
	if destroy < 0 {
		t.Fatal("missing Provider.destroy")
	}
	tail := body[destroy:]
	if !strings.Contains(tail, "p.mountDataset(vol)") {
		t.Fatal("failed zfs destroy must remount so squashfs jobs do not see ENOENT")
	}
	if !strings.Contains(tail, "remount after failed destroy") {
		t.Fatal("remount error must be chained onto the original destroy error")
	}
}

func TestVolumeEnsureMounted(t *testing.T) {
	src, err := os.ReadFile("zfs.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "func (v *zfsVolume) EnsureMounted()") {
		t.Fatal("squashfs volumes must expose EnsureMounted after a failed GC unmount")
	}
}
