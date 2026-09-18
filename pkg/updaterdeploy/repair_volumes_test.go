package updaterdeploy

import (
	"errors"
	"testing"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/volume"
)

func TestIsTrackedAppVolume(t *testing.T) {
	if isTrackedAppVolume(nil) {
		t.Fatal("nil info should not be tracked")
	}
	if isTrackedAppVolume(&volume.Info{}) {
		t.Fatal("empty meta should not be tracked")
	}
	if isTrackedAppVolume(&volume.Info{Meta: map[string]string{"other": "x"}}) {
		t.Fatal("non-controller volume should not be tracked")
	}
	if !isTrackedAppVolume(&volume.Info{Meta: map[string]string{"flynn-controller.app": "app-id"}}) {
		t.Fatal("controller app volume should be tracked")
	}
}

type fakeVolumeController struct {
	controller.Client
	volumes    []*ct.Volume
	jobs       map[string][]*ct.Job
	put        []*ct.Volume
	jobListErr error
}

func (f *fakeVolumeController) VolumeList() ([]*ct.Volume, error) { return f.volumes, nil }
func (f *fakeVolumeController) PutVolume(v *ct.Volume) error {
	cp := *v
	f.put = append(f.put, &cp)
	return nil
}
func (f *fakeVolumeController) JobList(appID string) ([]*ct.Job, error) {
	if f.jobListErr != nil {
		return nil, f.jobListErr
	}
	return f.jobs[appID], nil
}

func TestRepairStaleVolumes_RequiresTwoConsecutiveMisses(t *testing.T) {
	origDelay := staleVolumeRecheckDelay
	staleVolumeRecheckDelay = 0
	defer func() { staleVolumeRecheckDelay = origDelay }()

	jobID := "job-live"
	volID := "vol-1"
	ctrl := &fakeVolumeController{
		volumes: []*ct.Volume{{
			ID:     volID,
			HostID: "host-1",
			AppID:  "app-1",
			JobID:  &jobID,
			State:  ct.VolumeStateCreated,
		}},
		jobs: map[string][]*ct.Job{
			"app-1": {{ID: jobID, State: ct.JobStateDown}},
		},
	}

	// With no hosts answering ListVolumes, host index lacks host-1 → skip.
	if err := RepairStaleVolumes(ctrl, nil, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.put) != 0 {
		t.Fatal("should not destroy when host did not answer ListVolumes")
	}
}

func TestShouldDestroyStaleVolume(t *testing.T) {
	jobID := "job-1"
	vol := &ct.Volume{ID: "vol-1", HostID: "host-1", JobID: &jobID, State: ct.VolumeStateCreated}
	present := map[string]map[string]struct{}{"host-1": {"vol-1": {}}}
	missing := map[string]map[string]struct{}{"host-1": {}}
	noHost := map[string]map[string]struct{}{}
	live := map[string]bool{jobID: true}
	none := map[string]bool{}

	tests := []struct {
		name     string
		vol      *ct.Volume
		first    map[string]map[string]struct{}
		second   map[string]map[string]struct{}
		liveJobs map[string]bool
		want     bool
	}{
		{"nil volume", nil, missing, missing, none, false},
		{"already destroyed", &ct.Volume{ID: "vol-1", HostID: "host-1", State: ct.VolumeStateDestroyed}, missing, missing, none, false},
		{"host missing first poll", vol, noHost, missing, none, false},
		{"host missing second poll", vol, missing, noHost, none, false},
		{"present on first poll", vol, present, missing, none, false},
		{"present on second poll", vol, missing, present, none, false},
		{"missing twice with live job", vol, missing, missing, live, false},
		{"missing twice JobList failed", vol, missing, missing, nil, false},
		{"missing twice down job", vol, missing, missing, none, true},
		{"missing twice no job id", &ct.Volume{ID: "vol-1", HostID: "host-1", State: ct.VolumeStateCreated}, missing, missing, none, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldDestroyStaleVolume(tc.vol, tc.first, tc.second, tc.liveJobs)
			if got != tc.want {
				t.Fatalf("shouldDestroyStaleVolume() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRepairStaleVolumes_NeverDestroysVolumeWithLiveJob(t *testing.T) {
	origDelay := staleVolumeRecheckDelay
	staleVolumeRecheckDelay = 0
	defer func() { staleVolumeRecheckDelay = origDelay }()

	jobID := "job-live"
	ctrl := &fakeVolumeController{
		volumes: []*ct.Volume{{
			ID:     "vol-1",
			HostID: "host-1",
			AppID:  "app-1",
			JobID:  &jobID,
			State:  ct.VolumeStateCreated,
		}},
		jobs: map[string][]*ct.Job{
			"app-1": {{ID: jobID, State: ct.JobStateUp}},
		},
	}

	live, err := liveJobIDs(ctrl, ctrl.volumes)
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]map[string]struct{}{"host-1": {}}
	if shouldDestroyStaleVolume(ctrl.volumes[0], missing, missing, live) {
		t.Fatal("must not destroy a volume referenced by an up job")
	}
}

func TestRepairStaleVolumes_JobListErrorDoesNotDestroy(t *testing.T) {
	origDelay := staleVolumeRecheckDelay
	staleVolumeRecheckDelay = 0
	defer func() { staleVolumeRecheckDelay = origDelay }()

	jobID := "job-live"
	ctrl := &fakeVolumeController{
		volumes: []*ct.Volume{{
			ID:     "vol-1",
			HostID: "host-1",
			AppID:  "app-1",
			JobID:  &jobID,
			State:  ct.VolumeStateCreated,
		}},
		jobListErr: errors.New("controller unavailable"),
	}
	if err := RepairStaleVolumes(ctrl, nil, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.put) != 0 {
		t.Fatal("JobList failure must not mark volumes destroyed")
	}
	missing := map[string]map[string]struct{}{"host-1": {}}
	if shouldDestroyStaleVolume(ctrl.volumes[0], missing, missing, nil) {
		t.Fatal("nil liveJobs (JobList failed) must not destroy")
	}
}

func TestLiveJobIDs(t *testing.T) {
	ctrl := &fakeVolumeController{
		jobs: map[string][]*ct.Job{
			"app-1": {
				{ID: "up", State: ct.JobStateUp},
				{ID: "starting", State: ct.JobStateStarting},
				{ID: "down", State: ct.JobStateDown},
			},
		},
	}
	live, err := liveJobIDs(ctrl, []*ct.Volume{{AppID: "app-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !live["up"] || !live["starting"] || live["down"] {
		t.Fatalf("live=%v", live)
	}
}
