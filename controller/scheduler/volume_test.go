package main

import (
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
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

// The host answers CreateVolume with the size it gave the volume (the 20 GiB
// default when the request had none). The scheduler persists that size, so
// the controller's volume table shows the real capacity (NEW-28).
func TestCreatedVolumeIsPersistedWithHostSize(t *testing.T) {
	for name, req := range map[string]int64{"default": 0, "requested": 3 * 1024 * 1024 * 1024} {
		t.Run(name, func(t *testing.T) {
			want := req
			if want == 0 {
				want = volume.DefaultSize
			}
			log := log15.New()
			c := testutils.NewFakeHostClient("h1", false)
			h := NewHost(c, log)
			leader := true
			s := &Scheduler{
				isLeader:          &leader,
				logger:            log,
				jobs:              Jobs{},
				volumes:           map[string]*Volume{},
				hostVolumeIDs:     map[string]map[string]struct{}{},
				hosts:             map[string]*Host{"h1": h},
				controllerPersist: make(chan interface{}, 8),
				placementRequests: make(chan *PlacementRequest),
			}
			persisted := make(chan *ct.Volume, 8)
			go func() {
				for v := range s.controllerPersist {
					if vol, ok := v.(*ct.Volume); ok {
						persisted <- vol
					}
				}
			}()
			go func() {
				for req := range s.placementRequests {
					s.HandlePlacementRequest(req)
				}
			}()
			job := persistSingletonTestJob("redis")
			proc := job.Formation.ExpandedFormation.Release.Processes["redis"]
			proc.Volumes[0].Size = req
			markPlaceable(job)
			job.ID = "job"
			job.State = JobStatePending
			done := make(chan struct{})
			go func() {
				s.StartJob(job)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("StartJob did not finish")
			}
			for {
				select {
				case vol := <-persisted:
					if vol.State != ct.VolumeStateCreated {
						continue
					}
					if vol.Size != want {
						t.Fatalf("persisted size %d, want %d", vol.Size, want)
					}
					return
				case <-time.After(3 * time.Second):
					t.Fatal("created volume was not persisted")
				}
			}
		})
	}
}
