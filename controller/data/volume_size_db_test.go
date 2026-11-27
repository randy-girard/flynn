package data

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

// The volumes table stores the capacity the host gave the volume (NEW-28).
// Before migration 103 it had no size column, so GET /volumes never showed
// one. The scheduler re-persists every host volume when it follows a host,
// which backfills rows from before 103; a later upsert without a size (an
// old scheduler, a legacy host volume) keeps the stored one.
func TestVolumeAddStoresSize(t *testing.T) {
	db := smallPoolDB(t, "controller_volume_size_test")
	apps := NewAppRepo(db, "example.com", NewRouteRepo(db))
	vols := NewVolumeRepo(db)
	app := lifecycleApp(t, apps, nil)

	const size int64 = 5 * 1024 * 1024 * 1024
	vol := lifecycleVolume(t, vols, app.ID)
	if got, _ := vols.Get(app.ID, vol.ID); got.Size != 0 {
		t.Fatalf("size %d for a volume persisted without one, want 0", got.Size)
	}

	vol.Size = size
	if err := vols.Add(vol); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		got, err := vols.Get(app.ID, vol.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Size != size {
			t.Fatalf("%s: Get size %d, want %d", when, got.Size, size)
		}
		for name, list := range map[string]func() ([]*ct.Volume, error){
			"List":    vols.List,
			"AppList": func() ([]*ct.Volume, error) { return vols.AppList(app.ID) },
		} {
			l, err := list()
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, v := range l {
				if v.ID == vol.ID {
					found = true
					if v.Size != size {
						t.Fatalf("%s: %s size %d, want %d", when, name, v.Size, size)
					}
				}
			}
			if !found {
				t.Fatalf("%s: %s missing the volume", when, name)
			}
		}
	}
	check("after backfill")

	vol.Size = 0
	if err := vols.Add(vol); err != nil {
		t.Fatal(err)
	}
	check("after an upsert without a size")
}
