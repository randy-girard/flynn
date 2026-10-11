package updaterdeploy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
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
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, nil, false, nil); err != nil {
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
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, nil, false, nil); err != nil {
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
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"discoverd": art}, nil, true, nil); err != nil {
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
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, nil, false, nil); err != nil {
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
	if err := replaceHostInfraJobs([]hostInfraHost{h}, map[string]*ct.Artifact{"flannel": art}, nil, false, nil); err != nil {
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
	if err := replaceHostInfraJobs([]hostInfraHost{h1, h2}, map[string]*ct.Artifact{"flannel": art}, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	if len(h1.added) != 1 || len(h2.added) != 1 {
		t.Fatal("each host must get a replacement")
	}
}

// fakeInfraController records formation changes. With hosts set it acts like
// the scheduler: a formation runs one job per host its tags allow and stops
// its jobs on hosts the tags exclude.
type fakeInfraController struct {
	app        *ct.App
	release    *ct.Release
	releases   []*ct.Release
	artifacts  map[string]*ct.Artifact
	formations map[string]*ct.Formation
	puts       []ct.Formation
	created    []*ct.Release
	hosts      []*fakeInfraHost
}

func newFakeInfraController(t *testing.T, oldLayer string) *fakeInfraController {
	old := squashArtifact(t, oldLayer)
	old.ID = "art-old"
	rel := &ct.Release{ID: "rel-old", ArtifactIDs: []string{"art-old"}}
	return &fakeInfraController{
		app:        &ct.App{ID: "app-flannel", Name: "flannel"},
		release:    rel,
		releases:   []*ct.Release{rel},
		artifacts:  map[string]*ct.Artifact{"art-old": old},
		formations: map[string]*ct.Formation{"rel-old": {AppID: "app-flannel", ReleaseID: "rel-old", Processes: map[string]int{"app": 1}}},
	}
}

func (c *fakeInfraController) GetApp(name string) (*ct.App, error) {
	if name != c.app.Name {
		return nil, controller.ErrNotFound
	}
	return c.app, nil
}
func (c *fakeInfraController) GetAppRelease(string) (*ct.Release, error) { return c.release, nil }
func (c *fakeInfraController) GetArtifact(id string) (*ct.Artifact, error) {
	if a, ok := c.artifacts[id]; ok {
		return a, nil
	}
	return nil, controller.ErrNotFound
}
func (c *fakeInfraController) CreateArtifact(a *ct.Artifact) error {
	c.artifacts[a.ID] = a
	return nil
}
func (c *fakeInfraController) AppReleaseList(string) ([]*ct.Release, error) { return c.releases, nil }
func (c *fakeInfraController) CreateRelease(_ string, r *ct.Release) error {
	r.ID = fmt.Sprintf("rel-new-%d", len(c.created)+1)
	c.created = append(c.created, r)
	c.releases = append(c.releases, r)
	return nil
}
func (c *fakeInfraController) GetFormation(_, releaseID string) (*ct.Formation, error) {
	if f, ok := c.formations[releaseID]; ok {
		return f, nil
	}
	return nil, controller.ErrNotFound
}
func (c *fakeInfraController) SetAppRelease(_, releaseID string) error {
	for _, r := range c.releases {
		if r.ID == releaseID {
			c.release = r
			return nil
		}
	}
	return controller.ErrNotFound
}

func (c *fakeInfraController) PutFormation(f *ct.Formation) error {
	stored := *f
	c.puts = append(c.puts, stored)
	c.formations[f.ReleaseID] = &stored
	var rel *ct.Release
	for _, r := range c.releases {
		if r.ID == f.ReleaseID {
			rel = r
		}
	}
	for _, h := range c.hosts {
		tags := f.Tags["app"]
		allowed := f.Processes["app"] > 0 && ct.HostIDsTagMatches(tags, h.id) && ct.AvoidHostIDsTagMatches(tags, h.id)
		running := false
		for id, j := range h.jobs {
			if j.Status != host.StatusRunning || j.Job.Metadata[hostInfraReleaseKey] != f.ReleaseID {
				continue
			}
			if allowed {
				running = true
			} else {
				j.Status = host.StatusDone
				h.jobs[id] = j
			}
		}
		if allowed && !running && findRunningHostInfraJob(h.jobs, "flannel") == nil {
			id := fmt.Sprintf("%s-sched-%d", h.id, len(c.puts))
			h.jobs[id] = host.ActiveJob{Status: host.StatusRunning, Job: &host.Job{
				ID:         id,
				Metadata:   map[string]string{"flynn-controller.app_name": "flannel", hostInfraReleaseKey: f.ReleaseID},
				Mountspecs: []*host.Mountspec{{ID: jobLayerIDsForArtifact(c.artifacts[rel.ArtifactIDs[0]])}},
			}}
		}
	}
	return nil
}

func jobLayerIDsForArtifact(a *ct.Artifact) string {
	ids := artifactLayerIDs(a)
	if len(ids) != 1 {
		return ""
	}
	return ids[0]
}

func infraJob(id, release, layer string) host.ActiveJob {
	return host.ActiveJob{Status: host.StatusRunning, Job: &host.Job{
		ID:         id,
		Metadata:   map[string]string{"flynn-controller.app_name": "flannel", hostInfraReleaseKey: release},
		Mountspecs: []*host.Mountspec{{ID: layer}},
	}}
}

func newInfraImage(t *testing.T) *ct.Artifact {
	img := squashArtifact(t, "layer-new")
	img.ID = "art-new"
	return img
}

func runHostInfraRollout(t *testing.T, c *fakeInfraController, hosts []hostInfraHost, img *ct.Artifact) {
	t.Helper()
	grace, delay := hostInfraSchedulerGrace, hostInfraFormationRetryDelay
	hostInfraSchedulerGrace, hostInfraFormationRetryDelay = 0, 0
	t.Cleanup(func() { hostInfraSchedulerGrace, hostInfraFormationRetryDelay = grace, delay })
	images := map[string]*ct.Artifact{"flannel": img}
	r, err := prepareHostInfraRollout(c, "flannel", img, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceHostInfraJobs(hosts, images, map[string]*hostInfraRollout{"flannel": r}, false, nil); err != nil {
		t.Fatal(err)
	}
}

func assertHostRunsRelease(t *testing.T, h *fakeInfraHost, release string) {
	t.Helper()
	j := findRunningHostInfraJob(h.jobs, "flannel")
	if j == nil {
		t.Fatalf("%s: no running flannel job", h.id)
	}
	if j.Metadata[hostInfraReleaseKey] != release || jobLayerIDs(j)[0] != "layer-new" {
		t.Fatalf("%s: running release=%s layers=%v, want %s on layer-new", h.id, j.Metadata[hostInfraReleaseKey], jobLayerIDs(j), release)
	}
	running := 0
	for _, a := range h.jobs {
		if a.Status == host.StatusRunning {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("%s: %d running flannel jobs, want 1", h.id, running)
	}
}

// A reboot makes the scheduler restart host-infra jobs from the app's
// release, so the update must leave that release on the pulled image.
func TestReplaceHostInfraJobsMovesAppToPulledImageRelease(t *testing.T) {
	c := newFakeInfraController(t, "layer-old")
	h1 := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-old", "layer-old")}}
	h2 := &fakeInfraHost{id: "n2", jobs: map[string]host.ActiveJob{"b": infraJob("b", "rel-old", "layer-old")}}
	c.hosts = []*fakeInfraHost{h1, h2}

	runHostInfraRollout(t, c, []hostInfraHost{h1, h2}, newInfraImage(t))

	if len(c.created) != 1 || c.created[0].ArtifactIDs[0] != "art-new" {
		t.Fatalf("created=%+v, want one release on art-new", c.created)
	}
	newID := c.created[0].ID
	if c.release.ID != newID {
		t.Fatalf("app release=%s, want %s", c.release.ID, newID)
	}
	if n := c.formations["rel-old"].Processes["app"]; n != 0 {
		t.Fatalf("old formation app=%d, want 0", n)
	}
	if f := c.formations[newID]; f.Processes["app"] != 1 || len(f.Tags) != 0 {
		t.Fatalf("new formation=%+v, want app=1 on every host", f)
	}
	assertHostRunsRelease(t, h1, newID)
	assertHostRunsRelease(t, h2, newID)

	want := []string{
		"rel-old avoid=n1", newID + " hosts=n1",
		"rel-old avoid=n1,n2", newID + " hosts=n1,n2",
		newID + " all", "rel-old zero",
	}
	var got []string
	for _, f := range c.puts {
		tags := f.Tags["app"]
		switch {
		case f.Processes["app"] == 0:
			got = append(got, f.ReleaseID+" zero")
		case tags[ct.FormationAvoidHostIDsTag] != "":
			got = append(got, f.ReleaseID+" avoid="+tags[ct.FormationAvoidHostIDsTag])
		case tags[ct.FormationHostIDsTag] != "":
			got = append(got, f.ReleaseID+" hosts="+tags[ct.FormationHostIDsTag])
		default:
			got = append(got, f.ReleaseID+" all")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formation changes=%v\nwant %v", got, want)
	}
	if len(h1.added) != 0 || len(h2.added) != 0 {
		t.Fatal("scheduler started the new jobs; the updater must not add duplicates")
	}
}

// Jobs already on the pulled layers but tagged with the old release (left by
// earlier updaters) must still move, or the old release restarts on reboot.
func TestReplaceHostInfraJobsMovesJobOnNewLayersButOldRelease(t *testing.T) {
	c := newFakeInfraController(t, "layer-old")
	h := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-old", "layer-new")}}
	c.hosts = []*fakeInfraHost{h}

	runHostInfraRollout(t, c, []hostInfraHost{h}, newInfraImage(t))

	if len(h.stopped) == 0 || h.stopped[0] != "a" {
		t.Fatalf("stopped=%v, want old-release job a stopped", h.stopped)
	}
	assertHostRunsRelease(t, h, c.created[0].ID)
}

// If the scheduler does not start the new job, the updater starts it itself
// under the new release.
func TestReplaceHostInfraJobsStartsNewReleaseJobWithoutScheduler(t *testing.T) {
	c := newFakeInfraController(t, "layer-old")
	h := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-old", "layer-old")}}

	runHostInfraRollout(t, c, []hostInfraHost{h}, newInfraImage(t))

	newID := c.created[0].ID
	if len(h.added) != 1 || h.added[0].Metadata[hostInfraReleaseKey] != newID {
		t.Fatalf("added=%+v, want one job on %s", h.added, newID)
	}
	assertHostRunsRelease(t, h, newID)
}

// An app whose release already carries the pulled image keeps that release.
func TestReplaceHostInfraJobsKeepsReleaseWithPulledImage(t *testing.T) {
	c := newFakeInfraController(t, "layer-new")
	h := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-old", "layer-new")}}
	c.hosts = []*fakeInfraHost{h}

	runHostInfraRollout(t, c, []hostInfraHost{h}, newInfraImage(t))

	if len(c.created) != 0 || len(c.puts) != 0 || len(h.stopped) != 0 {
		t.Fatalf("created=%d puts=%d stopped=%v, want no changes", len(c.created), len(c.puts), h.stopped)
	}
}

// A rerun after an interrupted update reuses the release it created and
// leaves hosts that already moved alone.
func TestReplaceHostInfraJobsResumesInterruptedRollout(t *testing.T) {
	c := newFakeInfraController(t, "layer-old")
	c.artifacts["art-new"] = newInfraImage(t)
	next := &ct.Release{ID: "rel-next", ArtifactIDs: []string{"art-new"}}
	c.releases = append(c.releases, next)
	c.formations["rel-old"].Tags = map[string]map[string]string{"app": {ct.FormationAvoidHostIDsTag: "n1"}}
	c.formations["rel-next"] = &ct.Formation{ReleaseID: "rel-next", Processes: map[string]int{"app": 1},
		Tags: map[string]map[string]string{"app": {ct.FormationHostIDsTag: "n1"}}}
	h1 := &fakeInfraHost{id: "n1", jobs: map[string]host.ActiveJob{"a": infraJob("a", "rel-next", "layer-new")}}
	h2 := &fakeInfraHost{id: "n2", jobs: map[string]host.ActiveJob{"b": infraJob("b", "rel-old", "layer-old")}}
	c.hosts = []*fakeInfraHost{h1, h2}

	runHostInfraRollout(t, c, []hostInfraHost{h1, h2}, newInfraImage(t))

	if len(c.created) != 0 {
		t.Fatalf("created %d releases, want reuse of rel-next", len(c.created))
	}
	if len(h1.stopped) != 0 {
		t.Fatalf("n1 stopped=%v, already on rel-next", h1.stopped)
	}
	if c.release.ID != "rel-next" {
		t.Fatalf("app release=%s, want rel-next", c.release.ID)
	}
	assertHostRunsRelease(t, h1, "rel-next")
	assertHostRunsRelease(t, h2, "rel-next")
}
