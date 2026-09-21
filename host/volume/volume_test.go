package volume

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestInfoSizeMissingJSONIsZero(t *testing.T) {
	var info Info
	if err := json.Unmarshal([]byte(`{"id":"legacy-vol","type":"data"}`), &info); err != nil {
		t.Fatal(err)
	}
	if info.Size != 0 {
		t.Fatalf("legacy volume JSON should leave Size unset, got %d", info.Size)
	}
}

func TestInfoSizeRoundTrip(t *testing.T) {
	orig := Info{ID: "vol", Type: VolumeTypeData, Size: DefaultSize}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var got Info
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Size != DefaultSize {
		t.Fatalf("Size=%d, want %d", got.Size, DefaultSize)
	}
}

func TestRefquotaBytesDefault(t *testing.T) {
	if got := RefquotaBytes(nil); got != DefaultSize {
		t.Fatalf("nil info: got %d, want %d", got, DefaultSize)
	}
	if got := RefquotaBytes(&Info{}); got != DefaultSize {
		t.Fatalf("zero size: got %d, want %d", got, DefaultSize)
	}
	if got := RefquotaBytes(&Info{Size: -1}); got != DefaultSize {
		t.Fatalf("negative size: got %d, want %d", got, DefaultSize)
	}
}

func TestRefquotaBytesOverride(t *testing.T) {
	const size int64 = 5 * 1024 * 1024 * 1024
	if got := RefquotaBytes(&Info{Size: size}); got != size {
		t.Fatalf("got %d, want %d", got, size)
	}
}

func TestDataFilesystemPropsDefaultRefquota(t *testing.T) {
	mount := "/var/lib/flynn/volumes/zfs/mnt/data/vol"
	props := DataFilesystemProps(mount, nil)
	if props["mountpoint"] != mount {
		t.Fatalf("mountpoint=%q", props["mountpoint"])
	}
	want := strconv.FormatInt(DefaultSize, 10)
	if props["refquota"] != want {
		t.Fatalf("refquota=%q, want %q (20 GiB)", props["refquota"], want)
	}
}

func TestDataFilesystemPropsOverride(t *testing.T) {
	const size int64 = 8 * 1024 * 1024 * 1024
	props := DataFilesystemProps("/mnt/vol", &Info{Size: size})
	if props["refquota"] != strconv.FormatInt(size, 10) {
		t.Fatalf("refquota=%q", props["refquota"])
	}
}

func TestZFSCreateDataVolumeArgsIncludeRefquota(t *testing.T) {
	const size int64 = 10 * 1024 * 1024 * 1024
	args := ZFSCreateDataVolumeArgs("flynn-default/data/abc", "/mnt/abc", &Info{Size: size})
	joined := strings.Join(args, " ")
	if args[0] != "create" {
		t.Fatalf("args=%v", args)
	}
	want := "-o refquota=" + strconv.FormatInt(size, 10)
	if !strings.Contains(joined, want) {
		t.Fatalf("missing %q in %v", want, args)
	}
	if !strings.Contains(joined, "-o mountpoint=/mnt/abc") {
		t.Fatalf("missing mountpoint in %v", args)
	}
	if args[len(args)-1] != "flynn-default/data/abc" {
		t.Fatalf("dataset last: %v", args)
	}
}

func TestDefaultSizeIs20GiB(t *testing.T) {
	if DefaultSize != 20*1024*1024*1024 {
		t.Fatalf("DefaultSize=%d, want 20 GiB", DefaultSize)
	}
}
