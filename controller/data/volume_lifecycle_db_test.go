package data

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/random"
)

func lifecycleApp(t *testing.T, apps *AppRepo, meta map[string]string) *ct.App {
	t.Helper()
	app := &ct.App{Name: "vol-" + random.String(8), Meta: meta}
	if err := apps.Add(app); err != nil {
		t.Fatal(err)
	}
	return app
}

func lifecycleVolume(t *testing.T, vols *VolumeRepo, appID string) *ct.Volume {
	t.Helper()
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
	if err := vols.Add(vol); err != nil {
		t.Fatal(err)
	}
	return vol
}

// The scheduler persists a volume with PUT /volumes, which upserts. The
// upsert used to update only job_id, so a destroyed volume stayed "created"
// in the table for good.
func TestVolumeAddPersistsDestroyedState(t *testing.T) {
	db := smallPoolDB(t, "controller_volume_lifecycle_0_test")
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	vols := NewVolumeRepo(db)
	app := lifecycleApp(t, apps, nil)
	vol := lifecycleVolume(t, vols, app.ID)

	vol.State = ct.VolumeStateDestroyed
	if err := vols.Add(vol); err != nil {
		t.Fatal(err)
	}
	got, err := vols.Get(app.ID, vol.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ct.VolumeStateDestroyed {
		t.Fatalf("state %q after persisting destroyed, want destroyed", got.State)
	}

	// destroyed is final: a late "created" from a stale scheduler view
	// must not bring the row back
	vol.State = ct.VolumeStateCreated
	if err := vols.Add(vol); err != nil {
		t.Fatal(err)
	}
	if got, _ := vols.Get(app.ID, vol.ID); got.State != ct.VolumeStateDestroyed {
		t.Fatalf("state %q after a late created, want destroyed", got.State)
	}
}

func TestDecommissionAppVolumes(t *testing.T) {
	db := smallPoolDB(t, "controller_volume_lifecycle_1_test")
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	vols := NewVolumeRepo(db)
	app := lifecycleApp(t, apps, nil)
	other := lifecycleApp(t, apps, nil)
	a1 := lifecycleVolume(t, vols, app.ID)
	a2 := lifecycleVolume(t, vols, app.ID)
	gone := lifecycleVolume(t, vols, app.ID)
	gone.State = ct.VolumeStateDestroyed
	if err := vols.Add(gone); err != nil {
		t.Fatal(err)
	}
	keep := lifecycleVolume(t, vols, other.ID)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecommissionAppVolumes(tx, app.ID)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, v := range out {
		ids[v.ID] = true
		if v.DecommissionedAt == nil {
			t.Fatalf("returned volume %s has no decommissioned_at", v.ID)
		}
	}
	if len(out) != 2 || !ids[a1.ID] || !ids[a2.ID] {
		t.Fatalf("decommissioned %v, want %s and %s", ids, a1.ID, a2.ID)
	}
	for _, v := range []*ct.Volume{a1, a2} {
		got, _ := vols.Get(app.ID, v.ID)
		if got.DecommissionedAt == nil {
			t.Fatalf("volume %s not decommissioned in the table", v.ID)
		}
	}
	if got, _ := vols.Get(other.ID, keep.ID); got.DecommissionedAt != nil {
		t.Fatal("decommissioned another app's volume")
	}
	if got, _ := vols.Get(app.ID, gone.ID); got.DecommissionedAt != nil {
		t.Fatal("decommissioned an already destroyed volume")
	}
	// the scheduler learns of a decommission from the volume event stream
	var n int
	if err := db.QueryRow("SELECT count(*) FROM events WHERE object_type = 'volume' AND object_id = $1 AND data->>'decommissioned_at' IS NOT NULL", a1.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d decommission events for %s, want 1", n, a1.ID)
	}

	// a second run (que retry) finds nothing left to do
	tx, _ = db.Begin()
	again, err := DecommissionAppVolumes(tx, app.ID)
	tx.Rollback()
	if err != nil || len(again) != 0 {
		t.Fatalf("second run: %v, %v", again, err)
	}
}

func TestOrphanedVolumeList(t *testing.T) {
	db := smallPoolDB(t, "controller_volume_lifecycle_2_test")
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	vols := NewVolumeRepo(db)

	live := lifecycleApp(t, apps, nil)
	deleted := lifecycleApp(t, apps, nil)
	kept := lifecycleApp(t, apps, map[string]string{ct.AppMetaKeepVolumes: "true"})
	recent := lifecycleApp(t, apps, nil)

	liveVol := lifecycleVolume(t, vols, live.ID)
	orphan := lifecycleVolume(t, vols, deleted.ID)
	decom := lifecycleVolume(t, vols, deleted.ID)
	if err := vols.Decommission(deleted.ID, decom); err != nil {
		t.Fatal(err)
	}
	keptVol := lifecycleVolume(t, vols, kept.ID)
	recentVol := lifecycleVolume(t, vols, recent.ID)

	for _, a := range []*ct.App{deleted, kept, recent} {
		if err := db.Exec("app_delete", a.ID); err != nil {
			t.Fatal(err)
		}
	}
	// deleted and kept were deleted two hours ago, recent just now
	if err := db.Exec("UPDATE apps SET deleted_at = now() - interval '2 hours' WHERE app_id = $1 OR app_id = $2", deleted.ID, kept.ID); err != nil {
		t.Fatal(err)
	}

	list, err := vols.ListOrphaned(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, v := range list {
		ids[v.ID] = true
	}
	if !ids[orphan.ID] {
		t.Fatalf("orphan %s not listed: %v", orphan.ID, ids)
	}
	for name, v := range map[string]*ct.Volume{"live app": liveVol, "decommissioned": decom, "keep-volumes app": keptVol, "inside grace": recentVol} {
		if ids[v.ID] {
			t.Fatalf("listed the %s volume %s", name, v.ID)
		}
	}
	list, err = vols.ListOrphaned(0)
	if err != nil {
		t.Fatal(err)
	}
	ids = map[string]bool{}
	for _, v := range list {
		ids[v.ID] = true
	}
	if !ids[recentVol.ID] || !ids[orphan.ID] || ids[keptVol.ID] {
		t.Fatalf("grace 0 listed %v", ids)
	}
}
