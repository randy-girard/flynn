package fixer

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/host/volume"
)

func TestIsMissingVolumeError(t *testing.T) {
	if !isMissingVolumeError(`job node2-abc required volume vol-id, but that volume does not exist`) {
		t.Fatal("expected missing volume error")
	}
	if isMissingVolumeError("connection refused") {
		t.Fatal("unexpected missing volume error")
	}
}

func TestShouldDecommissionStaleVolume(t *testing.T) {
	hostVols := map[string]struct{}{"on-host": {}}
	vol := &ct.Volume{ID: "on-host", Type: volume.VolumeTypeData}
	if shouldDecommissionStaleVolume(vol, hostVols) {
		t.Fatal("volume present on host should not be decommissioned")
	}
	vol = &ct.Volume{ID: "missing", Type: volume.VolumeTypeData}
	if !shouldDecommissionStaleVolume(vol, hostVols) {
		t.Fatal("missing data volume should be decommissioned")
	}
	vol = &ct.Volume{ID: "missing", Type: volume.VolumeTypeSquashfs}
	if shouldDecommissionStaleVolume(vol, hostVols) {
		t.Fatal("missing image volume should not be decommissioned")
	}
}

func TestDesiredDBPeers(t *testing.T) {
	if n := desiredDBPeers(3, nil, "postgres"); n != 3 {
		t.Fatalf("expected 3 peers, got %d", n)
	}
	formation := &ct.Formation{Processes: map[string]int{"mariadb": 2}}
	if n := desiredDBPeers(3, formation, "mariadb"); n != 2 {
		t.Fatalf("expected 2 peers from formation, got %d", n)
	}
	if n := defaultDBPeers(3); n != 3 {
		t.Fatalf("expected 3 default peers, got %d", n)
	}
}

func TestDesiredWebProcesses(t *testing.T) {
	if n := desiredWebProcesses(1); n != 1 {
		t.Fatalf("expected 1 web process, got %d", n)
	}
	if n := desiredWebProcesses(3); n != 2 {
		t.Fatalf("expected 2 web processes, got %d", n)
	}
}

func TestSireniaAPIService(t *testing.T) {
	if sireniaAPIService("postgres") != "postgres-api" {
		t.Fatal("unexpected postgres API service name")
	}
}
