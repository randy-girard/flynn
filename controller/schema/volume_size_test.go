package schema

import (
	"path/filepath"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
)

func loadRepoSchema(t *testing.T) {
	t.Helper()
	schemaCache = nil
	t.Cleanup(func() { schemaCache = nil })
	root, err := filepath.Abs(filepath.Join("..", "..", "schema"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Load(root); err != nil {
		t.Fatal(err)
	}
}

const fiveGiB = 5 * 1024 * 1024 * 1024

// A datastore plugin sets VolumeReq.Size from the database runtime's disk.
// The release schema must keep accepting it.
func TestReleaseAcceptsVolumeSize(t *testing.T) {
	loadRepoSchema(t)
	rel := &ct.Release{
		ArtifactIDs: []string{"00000000-0000-0000-0000-000000000001"},
		Processes: map[string]ct.ProcessType{
			"redis": {Volumes: []ct.VolumeReq{{Path: "/data", Size: fiveGiB}}},
		},
	}
	if err := Validate(rel); err != nil {
		t.Fatalf("release with volume size rejected: %v", err)
	}
}

// The scheduler persists ct.Volume, which embeds VolumeReq. ZFS reports the
// refquota as Size for every volume, so PUT /volumes/:id always carries it.
func TestVolumeAcceptsSize(t *testing.T) {
	loadRepoSchema(t)
	vol := &ct.Volume{
		VolumeReq: ct.VolumeReq{Path: "/data", Size: volume.DefaultSize},
		ID:        "00000000-0000-0000-0000-000000000002",
		HostID:    "host1",
		Type:      volume.VolumeTypeData,
		State:     ct.VolumeStateCreated,
	}
	if err := Validate(vol); err != nil {
		t.Fatalf("volume with size rejected: %v", err)
	}
}

func TestVolumeRejectsNegativeSize(t *testing.T) {
	loadRepoSchema(t)
	vol := &ct.Volume{
		VolumeReq: ct.VolumeReq{Path: "/data", Size: -1},
		ID:        "00000000-0000-0000-0000-000000000002",
		HostID:    "host1",
		Type:      volume.VolumeTypeData,
		State:     ct.VolumeStateCreated,
	}
	if err := Validate(vol); err == nil {
		t.Fatal("negative volume size accepted")
	}
}
