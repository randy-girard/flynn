package updaterdeploy

import (
	"encoding/json"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
)

type fakeInfraHost struct {
	id          string
	jobs        map[string]host.ActiveJob
	stopped     []string
	added       []*host.Job
	restartOnce *host.ActiveJob
}

func (f *fakeInfraHost) ID() string { return f.id }

func (f *fakeInfraHost) ListJobs() (map[string]host.ActiveJob, error) {
	return f.jobs, nil
}

func (f *fakeInfraHost) StopJob(id string) error {
	f.stopped = append(f.stopped, id)
	j := f.jobs[id]
	j.Status = host.StatusDone
	f.jobs[id] = j
	if f.restartOnce != nil && f.restartOnce.Job != nil {
		r := *f.restartOnce
		f.jobs[r.Job.ID] = r
		f.restartOnce = nil
	}
	return nil
}

func (f *fakeInfraHost) AddJob(job *host.Job) error {
	f.added = append(f.added, job)
	f.jobs[job.ID] = host.ActiveJob{Job: job, Status: host.StatusRunning}
	return nil
}

func (f *fakeInfraHost) GetJob(id string) (*host.ActiveJob, error) {
	j := f.jobs[id]
	return &j, nil
}

func squashArtifact(t *testing.T, layerID string) *ct.Artifact {
	t.Helper()
	m := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{
				ID:     layerID,
				Type:   ct.ImageLayerTypeSquashfs,
				Length: 10,
			}},
		}},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &ct.Artifact{Type: ct.ArtifactTypeFlynn, RawManifest: raw}
}

func TestReplaceHostInfraJobsSkipsMatchingLayers(t *testing.T) {
	art := squashArtifact(t, "layer-old")
	h := &fakeInfraHost{
		id: "devnode1",
		jobs: map[string]host.ActiveJob{
			"old": {
				Status: host.StatusRunning,
				Job: &host.Job{
					ID: "old",
					Metadata: map[string]string{
						"flynn-controller.app_name": "flannel",
					},
					Mountspecs: []*host.Mountspec{{ID: "layer-old"}},
				},
			},
		},
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.stopped) != 0 || len(h.added) != 0 {
		t.Fatal("matching layers must not restart flannel")
	}
}

func TestReplaceHostInfraJobsRestartsOnNewLayers(t *testing.T) {
	art := squashArtifact(t, "layer-new")
	h := &fakeInfraHost{
		id: "devnode1",
		jobs: map[string]host.ActiveJob{
			"old": {
				Status: host.StatusRunning,
				Job: &host.Job{
					ID: "old",
					Metadata: map[string]string{
						"flynn-controller.app_name": "flannel",
					},
					Config: host.ContainerConfig{
						Env: map[string]string{"FLYNN_JOB_ID": "old"},
					},
					Mountspecs: []*host.Mountspec{{ID: "layer-old"}},
				},
			},
		},
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.stopped) != 1 || h.stopped[0] != "old" {
		t.Fatalf("stopped=%v", h.stopped)
	}
	if len(h.added) != 1 {
		t.Fatalf("added=%d", len(h.added))
	}
	got := h.added[0]
	if got.ID == "old" {
		t.Fatal("replacement must use a new job id")
	}
	if len(got.Mountspecs) != 1 || got.Mountspecs[0].ID != "layer-new" {
		t.Fatalf("mountspecs=%+v", got.Mountspecs)
	}
	if got.Config.Env["FLYNN_JOB_ID"] != got.ID {
		t.Fatal("FLYNN_JOB_ID must match the new job")
	}
}

func TestReplaceHostInfraJobsForceRestartsMatchingLayers(t *testing.T) {
	art := squashArtifact(t, "layer-old")
	h := &fakeInfraHost{
		id: "devnode1",
		jobs: map[string]host.ActiveJob{
			"old": {
				Status: host.StatusRunning,
				Job: &host.Job{
					ID: "old",
					Metadata: map[string]string{
						"flynn-controller.app_name": "discoverd",
					},
					Mountspecs: []*host.Mountspec{{ID: "layer-old"}},
				},
			},
		},
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"discoverd": art}, true, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.stopped) != 1 || len(h.added) != 1 {
		t.Fatal("--force must replace host-infra even when layers match")
	}
}

func TestReplaceHostInfraJobsStopsSchedulerRestartOnOldLayers(t *testing.T) {
	art := squashArtifact(t, "layer-new")
	h := &fakeInfraHost{
		id: "devnode1",
		jobs: map[string]host.ActiveJob{
			"old": {
				Status: host.StatusRunning,
				Job: &host.Job{
					ID: "old",
					Metadata: map[string]string{
						"flynn-controller.app_name": "flannel",
					},
					Mountspecs: []*host.Mountspec{{ID: "layer-old"}},
				},
			},
		},
		restartOnce: &host.ActiveJob{
			Status: host.StatusRunning,
			Job: &host.Job{
				ID: "sched",
				Metadata: map[string]string{
					"flynn-controller.app_name": "flannel",
				},
				Mountspecs: []*host.Mountspec{{ID: "layer-old"}},
			},
		},
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.stopped) < 2 || h.stopped[0] != "old" || h.stopped[1] != "sched" {
		t.Fatalf("stopped=%v want old then scheduler restart", h.stopped)
	}
	if len(h.added) != 1 || h.added[0].Mountspecs[0].ID != "layer-new" {
		t.Fatalf("added=%+v", h.added)
	}
}

func TestReplaceHostInfraJobsAdoptsMatchingLayerJob(t *testing.T) {
	art := squashArtifact(t, "layer-new")
	h := &fakeInfraHost{
		id: "devnode1",
		jobs: map[string]host.ActiveJob{
			"old": {
				Status: host.StatusRunning,
				Job: &host.Job{
					ID: "old",
					Metadata: map[string]string{
						"flynn-controller.app_name": "flannel",
					},
					Mountspecs: []*host.Mountspec{{ID: "layer-old"}},
				},
			},
		},
		restartOnce: &host.ActiveJob{
			Status: host.StatusRunning,
			Job: &host.Job{
				ID: "sched",
				Metadata: map[string]string{
					"flynn-controller.app_name": "flannel",
				},
				Mountspecs: []*host.Mountspec{{ID: "layer-new"}},
			},
		},
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.added) != 0 {
		t.Fatal("matching-layer scheduler job must be adopted, not duplicated")
	}
}

func TestReplaceHostInfraJobsRollsHostsSequentially(t *testing.T) {
	art := squashArtifact(t, "layer-new")
	h1 := &fakeInfraHost{
		id: "n1",
		jobs: map[string]host.ActiveJob{
			"a": {Status: host.StatusRunning, Job: &host.Job{
				ID: "a", Metadata: map[string]string{"flynn-controller.app_name": "flannel"},
				Mountspecs: []*host.Mountspec{{ID: "old"}},
			}},
		},
	}
	h2 := &fakeInfraHost{
		id: "n2",
		jobs: map[string]host.ActiveJob{
			"b": {Status: host.StatusRunning, Job: &host.Job{
				ID: "b", Metadata: map[string]string{"flynn-controller.app_name": "flannel"},
				Mountspecs: []*host.Mountspec{{ID: "old"}},
			}},
		},
	}
	if err := replaceHostInfraJobs([]hostInfraHost{h1, h2}, map[string]*ct.Artifact{"flannel": art}, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(h1.added) != 1 || len(h2.added) != 1 {
		t.Fatal("each host must get a replacement")
	}
}
