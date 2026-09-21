package main

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
)

func TestVolumeInfoCopiesSize(t *testing.T) {
	const size int64 = 7 * 1024 * 1024 * 1024
	v := &Volume{
		Volume: ct.Volume{
			VolumeReq: ct.VolumeReq{Path: "/data", Size: size},
			ID:        "vol-1",
			Type:      volume.VolumeTypeData,
		},
	}
	info := v.Info()
	if info.Size != size {
		t.Fatalf("Info.Size=%d, want %d", info.Size, size)
	}
	if info.ID != "vol-1" {
		t.Fatalf("ID=%q", info.ID)
	}
}

func TestNewVolumeCopiesSizeFromInfo(t *testing.T) {
	const size int64 = 3 * 1024 * 1024 * 1024
	v := NewVolume(&volume.Info{
		ID:   "vol-2",
		Type: volume.VolumeTypeData,
		Size: size,
		Meta: map[string]string{"flynn-controller.path": "/data"},
	}, ct.VolumeStateCreated, "host-1")
	if v.Size != size {
		t.Fatalf("Size=%d, want %d", v.Size, size)
	}
	if v.Info().Size != size {
		t.Fatalf("Info.Size=%d, want %d", v.Info().Size, size)
	}
}
