package main

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestControllerJobSkipsNilVolumes(t *testing.T) {
	j := &Job{
		ID:    "uuid",
		JobID: "host-uuid",
		AppID: "app",
		Volumes: []*Volume{
			nil,
			{Volume: ct.Volume{ID: "vol-1"}},
		},
	}
	got := j.ControllerJob()
	if len(got.VolumeIDs) != 1 || got.VolumeIDs[0] != "vol-1" {
		t.Fatalf("VolumeIDs=%v, want [vol-1]", got.VolumeIDs)
	}
}
