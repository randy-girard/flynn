package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
)

// NEW-35: volumes of apps deleted before app deletion decommissioned them
// stay in the controller table, and gc kept every controller volume, so
// they were never collected. --deleted-apps stops keeping them unless a live
// job on a host still binds one.
func TestDropDeletedAppVolumes(t *testing.T) {
	keep := map[string]struct{}{"orphan": {}, "held": {}, "live-app": {}}
	jobKeep := map[string]struct{}{"held": {}}
	released := dropDeletedAppVolumes(keep, jobKeep, []*ct.Volume{
		{ID: "orphan", AppID: "a1"},
		{ID: "held", AppID: "a1"},
		nil,
		{ID: ""},
	})
	if len(released) != 1 || released[0].ID != "orphan" {
		t.Fatalf("released %+v, want only orphan", released)
	}
	if _, ok := keep["orphan"]; ok {
		t.Fatal("deleted app volume still kept")
	}
	for _, id := range []string{"held", "live-app"} {
		if _, ok := keep[id]; !ok {
			t.Fatalf("%s must stay kept", id)
		}
	}
}

func gcTargets() []gcTarget {
	return []gcTarget{
		{HostID: "h1", Volume: &volume.Info{ID: "orphan", Type: volume.VolumeTypeData, Size: 1 << 30}},
		{HostID: "h1", Volume: &volume.Info{ID: "keep", Type: volume.VolumeTypeData}},
		{HostID: "h2", Volume: &volume.Info{ID: "orphan2", Type: volume.VolumeTypeData, Size: 2 << 30}},
	}
}

func TestCollectVolumesDryRunDestroysNothing(t *testing.T) {
	var out bytes.Buffer
	var destroyed []string
	res := collectVolumes(gcTargets(), map[string]struct{}{"keep": {}}, true, func(t gcTarget) error {
		destroyed = append(destroyed, t.Volume.ID)
		return nil
	}, &out, nil)
	if len(destroyed) != 0 {
		t.Fatalf("dry run destroyed %v", destroyed)
	}
	if res.count != 2 || res.bytes != 3<<30 || res.failed {
		t.Fatalf("dry run result %+v, want 2 volumes, 3 GiB", res)
	}
	if !strings.Contains(out.String(), "Would delete data volume orphan on h1") || strings.Contains(out.String(), " keep ") {
		t.Fatalf("dry run output:\n%s", out.String())
	}
}

func TestCollectVolumesDestroysAndReportsFailures(t *testing.T) {
	var out bytes.Buffer
	var destroyed []string
	res := collectVolumes(gcTargets(), map[string]struct{}{"keep": {}}, false, func(t gcTarget) error {
		if t.Volume.ID == "orphan2" {
			return errors.New("dataset is busy")
		}
		destroyed = append(destroyed, t.Volume.ID)
		return nil
	}, &out, nil)
	if len(destroyed) != 1 || destroyed[0] != "orphan" {
		t.Fatalf("destroyed %v, want [orphan]", destroyed)
	}
	if !res.failed || res.count != 1 {
		t.Fatalf("result %+v, want 1 deleted and failed", res)
	}
	if !strings.Contains(out.String(), "could not delete data volume orphan2") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestVolumeGCUsageHasDeletedAppsAndDryRun(t *testing.T) {
	cmd, ok := commands["volume:gc"]
	if !ok {
		t.Fatal("volume:gc not registered")
	}
	for _, want := range []string{"--dry-run", "--deleted-apps", "--grace=<duration>"} {
		if !strings.Contains(cmd.usage, want) {
			t.Fatalf("volume:gc usage lacks %s", want)
		}
	}
}
