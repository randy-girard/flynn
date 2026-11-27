package app_deletion

import (
	"testing"

	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/random"
)

func (fx *fixture) addVolume(appID string) *ct.Volume {
	fx.t.Helper()
	vol := &ct.Volume{
		VolumeReq: ct.VolumeReq{Path: "/data"},
		ID:        random.UUID(),
		HostID:    "host1",
		Type:      volume.VolumeTypeData,
		State:     ct.VolumeStateCreated,
		AppID:     appID,
		ReleaseID: random.UUID(),
		JobType:   "redis",
	}
	if err := data.NewVolumeRepo(fx.db).Add(vol); err != nil {
		fx.t.Fatal(err)
	}
	return vol
}

func (fx *fixture) volume(appID, id string) *ct.Volume {
	fx.t.Helper()
	vol, err := data.NewVolumeRepo(fx.db).Get(appID, id)
	if err != nil {
		fx.t.Fatal(err)
	}
	return vol
}

// TestDeletionDecommissionsTheAppsVolumes is NEW-35: deleting a datastore
// instance app (flynn apps:destroy, or the provider deleting it on
// addons:destroy) left its data volumes live in the table for good, so
// nothing ever destroyed them. The deletion now decommissions them, which
// hands them to the scheduler to destroy, and lists them in the event.
func TestDeletionDecommissionsTheAppsVolumes(t *testing.T) {
	fx := newFixture(t)
	v1 := fx.addVolume(fx.app.ID)
	v2 := fx.addVolume(fx.app.ID)
	otherApp := &ct.App{Name: "other-" + random.String(8)}
	if err := fx.apps.Add(otherApp); err != nil {
		t.Fatal(err)
	}
	other := fx.addVolume(otherApp.ID)

	if err := fx.run(); err != nil {
		t.Fatal(err)
	}
	fx.assertCompleted()
	for _, v := range []*ct.Volume{v1, v2} {
		if fx.volume(fx.app.ID, v.ID).DecommissionedAt == nil {
			t.Fatalf("volume %s of the deleted app was not decommissioned", v.ID)
		}
	}
	if fx.volume(otherApp.ID, other.ID).DecommissionedAt != nil {
		t.Fatal("decommissioned another app's volume")
	}
	evs := fx.events()
	last := evs[len(evs)-1].AppDeletion
	if last == nil || len(last.DecommissionedVolumes) != 2 {
		t.Fatalf("app_deletion event does not list the 2 decommissioned volumes: %+v", last)
	}
}

// TestDeletionKeepsVolumesWhenAskedTo: an operator who wants the data kept
// sets flynn-controller.keep-volumes=true on the app before deleting it.
func TestDeletionKeepsVolumesWhenAskedTo(t *testing.T) {
	fx := newFixture(t)
	fx.client.app.Meta = map[string]string{ct.AppMetaKeepVolumes: "true"}
	v := fx.addVolume(fx.app.ID)

	if err := fx.run(); err != nil {
		t.Fatal(err)
	}
	if fx.live() != nil {
		t.Fatal("app row still live")
	}
	if fx.volume(fx.app.ID, v.ID).DecommissionedAt != nil {
		t.Fatal("decommissioned a volume of an app with keep-volumes")
	}
	evs := fx.events()
	if last := evs[len(evs)-1].AppDeletion; last == nil || len(last.DecommissionedVolumes) != 0 || len(last.KeptVolumes) != 1 {
		t.Fatalf("event should list 1 kept and 0 decommissioned volumes: %+v", last)
	}
}

// A failed step leaves the volumes alone: the app (and its jobs) may still
// be serving until a retry finishes.
func TestFailedDeletionLeavesVolumesLive(t *testing.T) {
	fx := newFixture(t)
	v := fx.addVolume(fx.app.ID)
	fx.client.fail["delete_release"] = true
	if err := fx.run(); err == nil {
		t.Fatal("expected the deletion to fail")
	}
	if fx.volume(fx.app.ID, v.ID).DecommissionedAt != nil {
		t.Fatal("decommissioned a volume although the deletion failed")
	}
}
