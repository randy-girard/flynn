package main

import (
	"errors"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/random"
)

type reapFixture struct {
	s         *Scheduler
	client    *testutils.FakeHostClient
	persisted chan *ct.Volume
}

func newReapFixture(t *testing.T) *reapFixture {
	t.Helper()
	log := log15.New()
	c := testutils.NewFakeHostClient("h1", false)
	leader := true
	fx := &reapFixture{client: c, persisted: make(chan *ct.Volume, 16)}
	fx.s = &Scheduler{
		isLeader:          &leader,
		logger:            log,
		jobs:              Jobs{},
		volumes:           map[string]*Volume{},
		hosts:             map[string]*Host{"h1": NewHost(c, log)},
		controllerPersist: make(chan interface{}, 16),
	}
	go func() {
		for v := range fx.s.controllerPersist {
			if vol, ok := v.(*ct.Volume); ok {
				fx.persisted <- vol
			}
		}
	}()
	return fx
}

// addDecommissioned adds a data volume on h1 (and on the fake host) that
// was decommissioned age ago.
func (fx *reapFixture) addDecommissioned(age time.Duration) *Volume {
	id := random.UUID()
	info := &volume.Info{ID: id, Type: volume.VolumeTypeData, Meta: map[string]string{"flynn-controller.app": "app1"}}
	fx.client.PutVolume(info)
	vol := NewVolume(info, ct.VolumeStateCreated, "h1")
	at := time.Now().Add(-age)
	vol.DecommissionedAt = &at
	fx.s.volumes[id] = vol
	return vol
}

func (fx *reapFixture) reap(t *testing.T) {
	t.Helper()
	fx.s.reapDecommissionedVolumes(time.Now())
	done := make(chan struct{})
	go func() { fx.s.volumeReaps.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("volume reap did not finish")
	}
}

func (fx *reapFixture) persistedState(t *testing.T, id string) ct.VolumeState {
	t.Helper()
	for {
		select {
		case v := <-fx.persisted:
			if v.ID == id {
				return v.State
			}
		case <-time.After(2 * time.Second):
			return ""
		}
	}
}

// TestReapDestroysDecommissionedVolume is NEW-35: a decommissioned volume
// (an app deletion, a failover, a review app) was only marked; nothing
// destroyed it on its host. The scheduler now destroys it once the grace
// period has passed and no job holds it, and records it destroyed.
func TestReapDestroysDecommissionedVolume(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
	fx.reap(t)
	if got := fx.client.DestroyedVolumes(); len(got) != 1 || got[0] != vol.ID {
		t.Fatalf("destroyed %v, want [%s]", got, vol.ID)
	}
	if vol.GetState() != ct.VolumeStateDestroyed {
		t.Fatalf("state %s, want destroyed", vol.GetState())
	}
	if st := fx.persistedState(t, vol.ID); st != ct.VolumeStateDestroyed {
		t.Fatalf("persisted state %q, want destroyed", st)
	}
}

func TestReapWaitsForTheGracePeriod(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(time.Second)
	fx.reap(t)
	if got := fx.client.DestroyedVolumes(); len(got) != 0 {
		t.Fatalf("destroyed %v inside the grace period", got)
	}
	if vol.GetState() == ct.VolumeStateDestroyed {
		t.Fatal("marked destroyed inside the grace period")
	}
}

func TestReapSkipsAVolumeALiveJobHolds(t *testing.T) {
	for _, held := range []string{"job id", "job volumes"} {
		t.Run(held, func(t *testing.T) {
			fx := newReapFixture(t)
			vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
			job := &Job{ID: "j1", State: JobStateRunning, HostID: "h1"}
			if held == "job id" {
				id := job.ID
				vol.JobID = &id
			} else {
				job.Volumes = []*Volume{vol}
			}
			fx.s.jobs = Jobs{job.ID: job}
			fx.reap(t)
			if got := fx.client.DestroyedVolumes(); len(got) != 0 {
				t.Fatalf("destroyed %v while a running job holds it", got)
			}
			// once the job stops, the next pass destroys it
			job.State = JobStateStopped
			fx.reap(t)
			if got := fx.client.DestroyedVolumes(); len(got) != 1 {
				t.Fatalf("destroyed %v after the job stopped, want 1", got)
			}
		})
	}
}

// The host destroy is idempotent from the scheduler's view: a volume already
// gone from the host (a manual volume:delete, an earlier attempt whose
// answer was lost) is recorded destroyed.
func TestReapTreatsAMissingHostVolumeAsDestroyed(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
	fx.client.RemoveVolume(vol.ID)
	fx.reap(t)
	if vol.GetState() != ct.VolumeStateDestroyed {
		t.Fatalf("state %s, want destroyed", vol.GetState())
	}
}

func TestReapKeepsAVolumeWhoseDestroyFailedForTheNextPass(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
	fx.client.OnDestroyVolume = func(string) error { return errors.New("dataset is busy") }
	fx.reap(t)
	if vol.GetState() == ct.VolumeStateDestroyed {
		t.Fatal("marked destroyed although the host destroy failed")
	}
	fx.client.OnDestroyVolume = nil
	fx.reap(t)
	if vol.GetState() != ct.VolumeStateDestroyed {
		t.Fatal("not destroyed on the next pass")
	}
}

func TestReapSkipsUnknownOrUnhealthyHostsAndFollowers(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
	fx.s.hosts["h1"].Healthy = false
	fx.reap(t)
	if len(fx.client.DestroyedVolumes()) != 0 {
		t.Fatal("destroyed a volume on an unhealthy host")
	}
	fx.s.hosts["h1"].Healthy = true
	notLeader := false
	fx.s.isLeader = &notLeader
	fx.reap(t)
	if len(fx.client.DestroyedVolumes()) != 0 {
		t.Fatal("a scheduler that is not the leader destroyed a volume")
	}
	if vol.GetState() == ct.VolumeStateDestroyed {
		t.Fatal("marked destroyed")
	}
}

// A volume that is not decommissioned is never touched, however old.
func TestReapIgnoresLiveVolumes(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
	vol.DecommissionedAt = nil
	fx.reap(t)
	if len(fx.client.DestroyedVolumes()) != 0 {
		t.Fatal("destroyed a volume that is not decommissioned")
	}
}

// Each volume sync runs the reap, so decommissioned volumes go without any
// other trigger.
func TestVolumeSyncReapsDecommissionedVolumes(t *testing.T) {
	fx := newReapFixture(t)
	vol := fx.addDecommissioned(2 * decommissionedVolumeGrace)
	infos, _ := fx.client.ListVolumes()
	fx.s.applyVolumeFetch(volumeFetch{snaps: []hostVolSnap{{
		hostID: "h1",
		epoch:  fx.s.hosts["h1"].volEpoch,
		ids:    map[string]struct{}{vol.ID: {}},
		infos:  infos,
	}}})
	fx.s.volumeReaps.Wait()
	if got := fx.client.DestroyedVolumes(); len(got) != 1 || got[0] != vol.ID {
		t.Fatalf("volume sync destroyed %v, want [%s]", got, vol.ID)
	}
}
