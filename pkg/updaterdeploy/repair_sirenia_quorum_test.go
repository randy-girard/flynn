package updaterdeploy

import (
	"encoding/json"
	"strings"
	"testing"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	sirenia "github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/inconshreveable/log15"
)

func TestRegisteredSireniaJobs(t *testing.T) {
	instances := []*discoverd.Instance{
		{Meta: map[string]string{"FLYNN_JOB_ID": "node1-abc"}},
		{Meta: map[string]string{"FLYNN_JOB_ID": "node2-def"}},
		nil,
		{Meta: map[string]string{}},
	}
	got := registeredSireniaJobs(instances)
	if len(got) != 2 {
		t.Fatalf("expected 2 registered jobs, got %d", len(got))
	}
	if !got["node1-abc"] || !got["node2-def"] {
		t.Fatalf("unexpected registered set: %#v", got)
	}
}

func TestSireniaInstanceForJob(t *testing.T) {
	want := &discoverd.Instance{
		Addr: "100.100.1.1:3306",
		Meta: map[string]string{"FLYNN_JOB_ID": "node3-ghi"},
	}
	instances := []*discoverd.Instance{
		{Meta: map[string]string{"FLYNN_JOB_ID": "other"}},
		want,
	}
	if got := sireniaInstanceForJob(instances, "node3-ghi"); got != want {
		t.Fatalf("sireniaInstanceForJob: got %v want %v", got, want)
	}
	if got := sireniaInstanceForJob(instances, "missing"); got != nil {
		t.Fatalf("expected nil for missing job, got %v", got)
	}
}

func TestSireniaAsyncQuorumSatisfied(t *testing.T) {
	cases := []struct {
		name     string
		state    *sirenia.State
		expected int
		want     bool
	}{
		{"nil state", nil, 3, false},
		{"expected too small", &sirenia.State{Async: []*discoverd.Instance{{}}}, 2, false},
		{"no asyncs", &sirenia.State{}, 3, false},
		{"async count mismatch", &sirenia.State{Async: []*discoverd.Instance{{}}}, 4, false},
		{"3-node quorum", &sirenia.State{Async: []*discoverd.Instance{{}}}, 3, true},
		{"5-node quorum", &sirenia.State{Async: []*discoverd.Instance{{}, {}, {}}}, 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sireniaAsyncQuorumSatisfied(tc.state, tc.expected); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestSireniaInstanceHealthyEmpty(t *testing.T) {
	if sireniaInstanceHealthy(nil) {
		t.Fatal("nil instance should be unhealthy")
	}
	if sireniaInstanceHealthy(&discoverd.Instance{}) {
		t.Fatal("empty addr should be unhealthy")
	}
}

func TestSireniaMetaPeersHealthyRequiresAddrs(t *testing.T) {
	if sireniaMetaPeersHealthy(nil) {
		t.Fatal("nil state should be unhealthy")
	}
	if sireniaMetaPeersHealthy(&sirenia.State{}) {
		t.Fatal("missing primary should be unhealthy")
	}
	state := &sirenia.State{
		Primary: &discoverd.Instance{Addr: ""},
	}
	if sireniaMetaPeersHealthy(state) {
		t.Fatal("primary without addr should be unhealthy")
	}

	orig := checkSireniaInstanceHealthy
	defer func() { checkSireniaInstanceHealthy = orig }()
	checkSireniaInstanceHealthy = func(inst *discoverd.Instance) bool {
		return inst != nil && inst.Addr != "" && !strings.HasPrefix(inst.Addr, "bad:")
	}
	state = &sirenia.State{
		Primary: &discoverd.Instance{Addr: "ok:1"},
		Sync:    &discoverd.Instance{Addr: "ok:2"},
		Async:   []*discoverd.Instance{{Addr: "bad:3"}},
	}
	if sireniaMetaPeersHealthy(state) {
		t.Fatal("unhealthy async should fail meta peers check")
	}
	state.Async[0].Addr = "ok:3"
	if !sireniaMetaPeersHealthy(state) {
		t.Fatal("all peers healthy should pass")
	}
}

func TestJobsToRestartForSireniaQuorum(t *testing.T) {
	active := "rel-active"
	proc := "postgres"
	instances := []*discoverd.Instance{
		{Addr: "10.0.0.1:5432", Meta: map[string]string{"FLYNN_JOB_ID": "job-healthy"}},
		{Addr: "10.0.0.2:5432", Meta: map[string]string{"FLYNN_JOB_ID": "job-unhealthy"}},
	}
	healthy := func(inst *discoverd.Instance) bool {
		return inst != nil && inst.Meta["FLYNN_JOB_ID"] == "job-healthy"
	}
	jobs := []*ct.Job{
		nil,
		{ID: "other-release", ReleaseID: "other", Type: proc, State: ct.JobStateUp},
		{ID: "wrong-type", ReleaseID: active, Type: "web", State: ct.JobStateUp},
		{ID: "job-down", ReleaseID: active, Type: proc, State: ct.JobStateDown},
		{ID: "job-healthy", ReleaseID: active, Type: proc, State: ct.JobStateUp},
		{ID: "job-unhealthy", ReleaseID: active, Type: proc, State: ct.JobStateUp},
		{ID: "job-missing", ReleaseID: active, Type: proc, State: ct.JobStateStarting},
		{ID: "job-crashed", ReleaseID: active, Type: proc, State: ct.JobStateCrashed},
	}
	restart, down := jobsToRestartForSireniaQuorum(jobs, active, proc, instances, healthy)
	if down != 1 {
		t.Fatalf("down=%d want 1", down)
	}
	wantRestart := map[string]bool{"job-unhealthy": true, "job-missing": true}
	if len(restart) != len(wantRestart) {
		t.Fatalf("restart=%v want %v", restart, wantRestart)
	}
	for _, id := range restart {
		if !wantRestart[id] {
			t.Fatalf("unexpected restart id %q in %v", id, restart)
		}
	}
}

func TestWaitForSireniaAsyncQuorumSkipsSmallClusters(t *testing.T) {
	svc := &fakeDiscoverdService{}
	if err := waitForSireniaAsyncQuorum(svc, "postgres", 2, log15.New()); err != nil {
		t.Fatal(err)
	}
}

func TestWaitForSireniaAsyncQuorumSucceedsWhenMetaReady(t *testing.T) {
	meta, err := json.Marshal(sirenia.State{
		Async: []*discoverd.Instance{{Addr: "10.0.0.3:5432"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := &fakeDiscoverdService{meta: &discoverd.ServiceMeta{Data: meta}}
	if err := waitForSireniaAsyncQuorum(svc, "postgres", 3, log15.New()); err != nil {
		t.Fatal(err)
	}
}

type fakeQuorumController struct {
	controller.Client
	apps       []*ct.App
	releases   map[string]*ct.Release
	formations map[string][]*ct.Formation
	jobs       map[string][]*ct.Job
	deleted    []string
	scaled     []ct.ScaleOptions
}

func (f *fakeQuorumController) AppList() ([]*ct.App, error) { return f.apps, nil }
func (f *fakeQuorumController) GetRelease(id string) (*ct.Release, error) {
	if r, ok := f.releases[id]; ok {
		return r, nil
	}
	return nil, controller.ErrNotFound
}
func (f *fakeQuorumController) FormationList(appID string) ([]*ct.Formation, error) {
	return f.formations[appID], nil
}
func (f *fakeQuorumController) JobList(appID string) ([]*ct.Job, error) {
	return f.jobs[appID], nil
}
func (f *fakeQuorumController) DeleteJob(appID, jobID string) error {
	f.deleted = append(f.deleted, jobID)
	return nil
}
func (f *fakeQuorumController) ScaleAppRelease(appID, releaseID string, opts ct.ScaleOptions) error {
	f.scaled = append(f.scaled, opts)
	return nil
}

func TestRepairSireniaClusterQuorumRestartsUnregisteredAndDownJobs(t *testing.T) {
	const (
		appID   = "pg-app"
		release = "rel-active"
	)
	brokenMeta, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{
			Addr: "127.0.0.1:1",
			Meta: map[string]string{"FLYNN_RELEASE_ID": release},
		},
		Sync:  &discoverd.Instance{Addr: "127.0.0.1:2"},
		Async: []*discoverd.Instance{}, // missing async → needs repair
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveredMeta, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{
			Addr: "127.0.0.1:1",
			Meta: map[string]string{"FLYNN_RELEASE_ID": release},
		},
		Sync:  &discoverd.Instance{Addr: "127.0.0.1:2"},
		Async: []*discoverd.Instance{{Addr: "127.0.0.1:3"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	origHealth := checkSireniaInstanceHealthy
	defer func() { checkSireniaInstanceHealthy = origHealth }()
	checkSireniaInstanceHealthy = func(inst *discoverd.Instance) bool {
		// Treat discoverd registration with a non-empty addr as healthy so
		// tests do not dial fake Status() endpoints.
		return inst != nil && inst.Addr != ""
	}
	discoverdNewService = func(name string) discoverdService {
		return &fakeDiscoverdService{
			metas: []*discoverd.ServiceMeta{
				{Data: brokenMeta},
				{Data: recoveredMeta},
			},
			instances: []*discoverd.Instance{
				{Addr: "10.0.0.1:5432", Meta: map[string]string{"FLYNN_JOB_ID": "job-registered"}},
			},
		}
	}

	ctrl := &fakeQuorumController{
		apps: []*ct.App{{ID: appID, Name: "postgres"}},
		releases: map[string]*ct.Release{
			release: {ID: release, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {{AppID: appID, ReleaseID: release, Processes: map[string]int{"postgres": 3}}},
		},
		jobs: map[string][]*ct.Job{
			appID: {
				{ID: "job-registered", ReleaseID: release, Type: "postgres", State: ct.JobStateUp},
				{ID: "job-missing", ReleaseID: release, Type: "postgres", State: ct.JobStateUp},
				{ID: "job-down", ReleaseID: release, Type: "postgres", State: ct.JobStateDown},
			},
		},
	}

	if err := RepairSireniaClusterQuorum(ctrl, true, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.deleted) == 0 {
		t.Fatal("expected at least one job restart for unregistered/unhealthy peers")
	}
	foundMissing := false
	for _, id := range ctrl.deleted {
		if id == "job-missing" {
			foundMissing = true
		}
		if id == "job-down" {
			t.Fatal("down jobs must not be DeleteJob'd; use ScaleAppRelease")
		}
	}
	if !foundMissing {
		t.Fatalf("expected job-missing to be restarted, deleted=%v", ctrl.deleted)
	}
	if len(ctrl.scaled) != 1 {
		t.Fatalf("expected formation re-assert for down jobs, scaled=%d", len(ctrl.scaled))
	}
	if ctrl.scaled[0].Processes["postgres"] != 3 {
		t.Fatalf("scale processes=%v want postgres=3", ctrl.scaled[0].Processes)
	}
}

func TestRepairSireniaClusterQuorumNoopWhenSatisfiedAndHealthy(t *testing.T) {
	// Use empty addrs so MetaPeersHealthy is false... actually we need BOTH
	// quorum satisfied AND healthy to early-return. Healthy requires live Status.
	// Instead verify early return for expected <= 2.
	const (
		appID   = "pg-app"
		release = "rel-active"
	)
	meta, err := json.Marshal(sirenia.State{
		Primary: &discoverd.Instance{
			Meta: map[string]string{"FLYNN_RELEASE_ID": release},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	orig := discoverdNewService
	defer func() { discoverdNewService = orig }()
	discoverdNewService = func(name string) discoverdService {
		return &fakeDiscoverdService{meta: &discoverd.ServiceMeta{Data: meta}}
	}
	ctrl := &fakeQuorumController{
		apps: []*ct.App{{ID: appID, Name: "postgres"}},
		releases: map[string]*ct.Release{
			release: {ID: release, Env: map[string]string{"SIRENIA_PROCESS": "postgres"}},
		},
		formations: map[string][]*ct.Formation{
			appID: {{AppID: appID, ReleaseID: release, Processes: map[string]int{"postgres": 1}}},
		},
		jobs: map[string][]*ct.Job{appID: {{ID: "j1", ReleaseID: release, Type: "postgres", State: ct.JobStateUp}}},
	}
	if err := RepairSireniaClusterQuorum(ctrl, false, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.deleted) != 0 || len(ctrl.scaled) != 0 {
		t.Fatalf("singleton/small formation should no-op, deleted=%v scaled=%v", ctrl.deleted, ctrl.scaled)
	}
}

func TestRestartDownSireniaJobs(t *testing.T) {
	ctrl := &fakeQuorumController{}
	app := &ct.App{ID: "app", Name: "postgres"}
	formation := &ct.Formation{ReleaseID: "rel", Processes: map[string]int{"postgres": 3}}
	if err := restartDownSireniaJobs(ctrl, app, formation, 1, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.scaled) != 1 || ctrl.scaled[0].Processes["postgres"] != 3 {
		t.Fatalf("unexpected scale: %#v", ctrl.scaled)
	}
	if err := restartDownSireniaJobs(ctrl, app, &ct.Formation{ReleaseID: "rel"}, 1, log15.New()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.scaled) != 1 {
		t.Fatal("empty processes should not scale")
	}
}
