package main

import (
	"testing"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/placement"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
)

func roleJob(app *ct.App, typ string, omni bool) *Job {
	if app == nil {
		app = &ct.App{ID: "app", Name: "web"}
	}
	if app.ID == "" {
		app.ID = app.Name
	}
	return &Job{
		ID:    app.ID + "-" + typ,
		Type:  typ,
		AppID: app.ID,
		State: JobStatePending,
		Formation: &Formation{
			OriginalProcesses: Processes{typ: 1},
			ExpandedFormation: &ct.ExpandedFormation{
				App: app,
				Release: &ct.Release{
					ID: "rel",
					Processes: map[string]ct.ProcessType{
						typ: {Omni: omni},
					},
				},
				Processes: map[string]int{typ: 1},
			},
		},
	}
}

func roleHost(id, role string) *Host {
	tags := map[string]string{}
	if role != "" {
		tags[placement.TagRole] = role
	}
	return &Host{ID: id, Healthy: true, Tags: tags}
}

func TestMixedPlacesUserJobOnUntaggedHost(t *testing.T) {
	untagged := roleHost("untagged", "")
	s := &Scheduler{
		jobs:  Jobs{},
		hosts: map[string]*Host{"untagged": untagged, "core": roleHost("core", placement.RoleCore)},
	}
	job := roleJob(&ct.App{ID: "web", Name: "web"}, "web", false)
	sawUntagged := false
	for i := 0; i < 30; i++ {
		got := s.pickHost(job, nil)
		if got == nil {
			t.Fatal("mixed must place a user job")
		}
		if got.ID == "untagged" {
			sawUntagged = true
		}
	}
	if !sawUntagged {
		t.Fatal("mixed must still place a user job on an untagged host")
	}
}

func TestSplitPlacesUserJobOnlyOnAppHost(t *testing.T) {
	s := &Scheduler{
		workloadPlacement: placement.ModeSplit,
		jobs:              Jobs{},
		hosts: map[string]*Host{
			"core":     roleHost("core", placement.RoleCore),
			"app":      roleHost("app", placement.RoleApp),
			"untagged": roleHost("untagged", ""),
		},
	}
	job := roleJob(&ct.App{ID: "web", Name: "web"}, "web", false)
	for i := 0; i < 20; i++ {
		got := s.pickHost(job, nil)
		if got == nil || got.ID != "app" {
			t.Fatalf("attempt %d: placed on %v, want the app host", i, got)
		}
	}
	builder := roleJob(&ct.App{ID: "web", Name: "web"}, "slugbuilder", false)
	got := s.pickHost(builder, nil)
	if got == nil || got.ID != "app" {
		t.Fatalf("slugbuilder on the user app placed on %v, want the app host", got)
	}
}

func TestSplitPlacesControllerWebOnCore(t *testing.T) {
	s := &Scheduler{
		workloadPlacement: placement.ModeSplit,
		jobs:              Jobs{},
		hosts: map[string]*Host{
			"core": roleHost("core", placement.RoleCore),
			"app":  roleHost("app", placement.RoleApp),
		},
	}
	job := roleJob(&ct.App{
		ID:   "controller",
		Name: "controller",
		Meta: map[string]string{"flynn-system-app": "true"},
	}, "web", false)
	for i := 0; i < 20; i++ {
		got := s.pickHost(job, nil)
		if got == nil || got.ID != "core" {
			t.Fatalf("attempt %d: controller web on %v, want core", i, got)
		}
	}
}

func TestSplitRouterStillPlacesOnAppHost(t *testing.T) {
	app := roleHost("app", placement.RoleApp)
	s := &Scheduler{
		workloadPlacement: placement.ModeSplit,
		jobs:              Jobs{},
		hosts:             map[string]*Host{"app": app},
	}
	job := roleJob(&ct.App{
		ID:   "router",
		Name: "router",
		Meta: map[string]string{"flynn-system-app": "true"},
	}, "app", true)
	if got := s.pickHost(job, nil); got != app {
		t.Fatalf("router on %v, want the app host", got)
	}
}

func TestSplitLeavesVolumeOnTheHostThatHasIt(t *testing.T) {
	leader := true
	job := roleJob(&ct.App{ID: "webapp", Name: "webapp"}, "web", false)
	job.Formation.Release.Processes["web"] = ct.ProcessType{Volumes: []ct.VolumeReq{{Path: "/data"}}}
	job.State = JobStatePending
	markPlaceable(job)
	vol := &Volume{Volume: ct.Volume{
		ID:        "vol",
		HostID:    "core",
		AppID:     job.AppID,
		ReleaseID: job.Formation.Release.ID,
		JobType:   job.Type,
		State:     ct.VolumeStateCreated,
		VolumeReq: ct.VolumeReq{Path: "/data"},
	}}
	s := &Scheduler{
		isLeader:          &leader,
		logger:            log15.New(),
		workloadPlacement: placement.ModeSplit,
		jobs:              Jobs{},
		hosts: map[string]*Host{
			"core": roleHost("core", placement.RoleCore),
			"app":  roleHost("app", placement.RoleApp),
		},
		volumes:           map[string]*Volume{vol.ID: vol},
		hostVolumeIDs:     map[string]map[string]struct{}{"core": {"vol": {}}},
		controllerPersist: make(chan interface{}, 4),
	}
	req := &PlacementRequest{Job: job, Err: make(chan error, 1)}
	s.HandlePlacementRequest(req)
	if err := <-req.Err; err != nil {
		t.Fatalf("volume must stay put: %v", err)
	}
	if req.Host == nil || req.Host.ID != "core" || job.HostID != "core" {
		t.Fatalf("host %v job host %q, want the volume host", req.Host, job.HostID)
	}
	if s.volumes["vol"] == nil || s.volumes["vol"].State == ct.VolumeStateDestroyed {
		t.Fatal("enabling split must not delete the volume")
	}
	if len(s.volumes) != 1 {
		t.Fatalf("volumes %+v, want the original only", s.volumes)
	}
}

func TestFlippingPlacementModeDoesNotStopRunningUserJob(t *testing.T) {
	job := roleJob(&ct.App{ID: "web", Name: "web"}, "web", false)
	job.HostID = "core"
	job.State = JobStateRunning
	job.Formation.App = &ct.App{ID: "web", Name: "web"}
	s := &Scheduler{
		logger: log15.New(),
		jobs:   Jobs{job.ID: job},
		hosts:  map[string]*Host{"core": roleHost("core", placement.RoleCore)},
		formations: Formations{
			job.Formation.key(): job.Formation,
		},
	}
	s.workloadPlacement = placement.ModeSplit
	s.stopJobsWithMismatchedTags(job.Formation)
	if job.State != JobStateRunning || job.HostID != "core" {
		t.Fatalf("state=%s host=%q; flipping the mode must not stop a running user job", job.State, job.HostID)
	}
}

func TestSplitDoesNotPromotePostgresWhenAddedHostsAreApp(t *testing.T) {
	cc := testutils.NewFakeControllerClient()
	app := &ct.App{
		ID:   "postgres",
		Name: "postgres",
		Meta: map[string]string{"flynn-system-app": "true", "flynn-datastore": "true"},
	}
	release := &ct.Release{
		ID:  "pg-single",
		Env: map[string]string{"SIRENIA_PROCESS": "postgres", "SINGLETON": "true"},
	}
	if err := cc.CreateApp(app); err != nil {
		t.Fatal(err)
	}
	if err := cc.CreateRelease(app.ID, release); err != nil {
		t.Fatal(err)
	}
	procs := map[string]int{"postgres": 1}
	if err := cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: procs}); err != nil {
		t.Fatal(err)
	}
	leader := true
	s := NewScheduler(testutils.NewFakeCluster(), cc, nil, log15.New())
	s.isLeader = &leader
	s.workloadPlacement = placement.ModeSplit
	s.formations.Add(NewFormation(&ct.ExpandedFormation{
		App:       app,
		Release:   release,
		Processes: procs,
	}))
	s.hosts = map[string]*Host{
		"core": roleHost("core", placement.RoleCore),
		"a1":   roleHost("a1", placement.RoleApp),
		"a2":   roleHost("a2", placement.RoleApp),
	}
	s.maybePromoteSireniaHA()
	active, err := cc.FormationListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Release.ID != release.ID || active[0].Release.Env["SINGLETON"] != "true" {
		t.Fatalf("platform postgres changed under split with one core host: %+v", active)
	}

	s.hosts["c2"] = roleHost("c2", placement.RoleCore)
	s.hosts["c3"] = roleHost("c3", placement.RoleCore)
	s.maybePromoteSireniaHA()
	active, err = cc.FormationListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Release.Env["SINGLETON"] == "true" {
		t.Fatal("three core hosts must still promote platform postgres")
	}
}

func TestApplyPlacementKeepsLastModeOnError(t *testing.T) {
	s := &Scheduler{logger: log15.New(), workloadPlacement: placement.ModeSplit}
	s.applyPlacement(s.logger, nil, true, errString("read failed"))
	if s.workloadPlacement != placement.ModeSplit {
		t.Fatalf("mode %q, want the last mode", s.workloadPlacement)
	}
	s.applyPlacement(s.logger, nil, false, errString("no getter"))
	if s.workloadPlacement != placement.ModeSplit {
		t.Fatalf("skipped read changed mode to %q", s.workloadPlacement)
	}
	if s.placementMode() != placement.ModeMixed {
		// empty is mixed; this scheduler already has a mode
	}
	fresh := &Scheduler{}
	if fresh.placementMode() != placement.ModeMixed {
		t.Fatalf("before the first read, mode is %q", fresh.placementMode())
	}
}

type errString string

func (e errString) Error() string { return string(e) }
