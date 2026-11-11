package main

import (
	"errors"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/random"
)

const singletonCapTag = ct.FormationMaxPersistentSingletonsTag

func TestPickHostPrefersLowestSingletonCount(t *testing.T) {
	// h1 has five other databases and zero redis. h2 has one redis.
	// Same-type count would pick h1. The singleton count must pick h2.
	s := &Scheduler{
		jobs:  Jobs{},
		hosts: map[string]*Host{"h1": {ID: "h1", Healthy: true}, "h2": {ID: "h2", Healthy: true}},
	}
	for i := 0; i < 5; i++ {
		job := persistSingletonTestJob("kafka")
		job.ID = "k"
		job.HostID = "h1"
		job.State = JobStateRunning
		s.jobs[string(rune('a'+i))] = job
	}
	redis := persistSingletonTestJob("redis")
	redis.ID = "redis"
	redis.HostID = "h2"
	redis.State = JobStateRunning
	s.jobs["redis"] = redis
	next := persistSingletonTestJob("redis")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{"h2": 1})
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: placed on %v, want h2 (one singleton, not the host with five)", i, got)
		}
	}
}

func TestPickHostStoppedSingletonDoesNotAttract(t *testing.T) {
	stopped := persistSingletonTestJob("redis")
	stopped.ID = "old"
	stopped.HostID = "h1"
	stopped.State = JobStateStopped
	running := persistSingletonTestJob("postgres")
	running.ID = "pg"
	running.HostID = "h2"
	running.State = JobStateRunning
	s := &Scheduler{
		jobs:  Jobs{"old": stopped, "pg": running},
		hosts: map[string]*Host{"h1": {ID: "h1", Healthy: true}, "h2": {ID: "h2", Healthy: true}},
	}
	next := persistSingletonTestJob("mysql")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{})
		if got == nil || got.ID != "h1" {
			t.Fatalf("attempt %d: placed on %v, want h1 (stopped redis does not count)", i, got)
		}
	}
}

func TestPickHostPendingSingletonCounts(t *testing.T) {
	pending := persistSingletonTestJob("redis")
	pending.ID = "pending"
	pending.HostID = "h1"
	pending.State = JobStatePending
	s := &Scheduler{
		jobs:  Jobs{"pending": pending},
		hosts: map[string]*Host{"h1": {ID: "h1", Healthy: true}, "h2": {ID: "h2", Healthy: true}},
	}
	next := persistSingletonTestJob("postgres")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{})
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: placed on %v, want h2 (pending redis on h1 counts)", i, got)
		}
	}
}

func TestPickHostSkipsHostAtPersistentSingletonCap(t *testing.T) {
	h1 := &Host{ID: "h1", Healthy: true, Tags: map[string]string{singletonCapTag: "1"}}
	h2 := &Host{ID: "h2", Healthy: true}
	running := persistSingletonTestJob("redis")
	running.ID = "redis"
	running.HostID = "h1"
	running.State = JobStateRunning
	s := &Scheduler{jobs: Jobs{"redis": running}, hosts: map[string]*Host{"h1": h1, "h2": h2}}
	next := persistSingletonTestJob("postgres")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{})
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: placed on %v, want h2 (h1 is at the singleton cap)", i, got)
		}
	}
}

func TestPickHostNonNumericSingletonCapIsUnlimited(t *testing.T) {
	h1 := &Host{ID: "h1", Healthy: true, Tags: map[string]string{singletonCapTag: "lots"}}
	h2 := &Host{ID: "h2", Healthy: true}
	running := persistSingletonTestJob("redis")
	running.ID = "redis"
	running.HostID = "h2"
	running.State = JobStateRunning
	s := &Scheduler{jobs: Jobs{"redis": running}, hosts: map[string]*Host{"h1": h1, "h2": h2}}
	next := persistSingletonTestJob("postgres")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{})
		if got == nil || got.ID != "h1" {
			t.Fatalf("attempt %d: placed on %v, want h1 (a non-integer cap is unlimited)", i, got)
		}
	}
}

func TestPickHostBlocksWhenEverySingletonCapIsFull(t *testing.T) {
	hosts := map[string]*Host{
		"h1": {ID: "h1", Healthy: true, Tags: map[string]string{singletonCapTag: "1"}},
		"h2": {ID: "h2", Healthy: true, Tags: map[string]string{singletonCapTag: "1"}},
	}
	s := &Scheduler{jobs: Jobs{}, hosts: hosts}
	for _, id := range []string{"h1", "h2"} {
		job := persistSingletonTestJob("redis")
		job.ID = id
		job.HostID = id
		job.State = JobStateRunning
		s.jobs[id] = job
	}
	if got := s.pickHost(persistSingletonTestJob("postgres"), map[string]int{}); got != nil {
		t.Fatalf("placed on %s, want no host", got.ID)
	}
}

func TestPickHostProcessCapBeforeSingletonScore(t *testing.T) {
	web := &Job{
		ID:     "web",
		HostID: "h1",
		Type:   "web",
		State:  JobStateRunning,
		Formation: NewFormation(&ct.ExpandedFormation{
			App:       &ct.App{ID: "shop"},
			Release:   &ct.Release{ID: "rel", Processes: map[string]ct.ProcessType{"web": {}}},
			Processes: Processes{"web": 1},
		}),
	}
	jobs := Jobs{"web": web}
	for i := 0; i < 5; i++ {
		job := persistSingletonTestJob("kafka")
		job.ID = string(rune('a' + i))
		job.HostID = "h2"
		job.State = JobStateRunning
		jobs[job.ID] = job
	}
	s := &Scheduler{
		jobs: jobs,
		hosts: map[string]*Host{
			"h1": {ID: "h1", Healthy: true, Tags: map[string]string{ct.FormationMaxProcessesTag: "1"}},
			"h2": {ID: "h2", Healthy: true},
		},
	}
	next := persistSingletonTestJob("redis")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{})
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: placed on %v, want h2 (h1 is at flynn-max-processes)", i, got)
		}
	}
}

func TestPickHostDoesNotScoreSireniaRollingPeer(t *testing.T) {
	peer := persistSingletonTestJob("postgres")
	peer.Formation.OriginalProcesses["postgres"] = 3
	peer.ID = "peer"
	peer.HostID = "h1"
	peer.State = JobStateRunning
	jobs := Jobs{"peer": peer}
	for i := 0; i < 3; i++ {
		job := persistSingletonTestJob("kafka")
		job.ID = string(rune('a' + i))
		job.HostID = "h2"
		job.State = JobStateRunning
		jobs[job.ID] = job
	}
	s := &Scheduler{
		jobs: jobs,
		hosts: map[string]*Host{
			"h1": {ID: "h1", Healthy: true},
			"h2": {ID: "h2", Healthy: true},
		},
	}
	next := persistSingletonTestJob("postgres")
	next.Formation.OriginalProcesses["postgres"] = 1
	next.Formation.Release.Env = map[string]string{"SIRENIA_PROCESS": "postgres"}
	if !next.isPersistentSingleton() || !next.spreadsAcrossHosts() {
		t.Fatal("rolling sirenia peer must stay a persistent singleton that spreads")
	}
	counts := map[string]int{"h1": spreadFormationWeight}
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, counts)
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: placed rolling peer on %v, want h2", i, got)
		}
	}
}

func TestFollowerSpreadsToEmptyHost(t *testing.T) {
	primary := persistSingletonTestJob("postgres")
	primary.ID = "primary"
	primary.HostID = "h1"
	primary.State = JobStateRunning
	s := &Scheduler{
		jobs:  Jobs{"primary": primary},
		hosts: map[string]*Host{"h1": {ID: "h1", Healthy: true}, "h2": {ID: "h2", Healthy: true}},
	}
	follower := persistSingletonTestJob("postgres")
	for i := 0; i < 20; i++ {
		got := s.pickHost(follower, map[string]int{})
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: follower on %v, want h2", i, got)
		}
	}
}

func TestFollowerStaysOnPrimaryWhenOtherHostIsAtCap(t *testing.T) {
	primary := persistSingletonTestJob("postgres")
	primary.ID = "primary"
	primary.HostID = "h1"
	primary.State = JobStateRunning
	jobs := Jobs{"primary": primary}
	for i := 0; i < 3; i++ {
		job := persistSingletonTestJob("redis")
		job.ID = string(rune('a' + i))
		job.HostID = "h2"
		job.State = JobStateRunning
		jobs[job.ID] = job
	}
	s := &Scheduler{
		jobs: jobs,
		hosts: map[string]*Host{
			"h1": {ID: "h1", Healthy: true, Tags: map[string]string{singletonCapTag: "2"}},
			"h2": {ID: "h2", Healthy: true, Tags: map[string]string{singletonCapTag: "3"}},
		},
	}
	follower := persistSingletonTestJob("postgres")
	for i := 0; i < 20; i++ {
		got := s.pickHost(follower, map[string]int{})
		if got == nil || got.ID != "h1" {
			t.Fatalf("attempt %d: follower on %v, want h1 (h2 is at the cap)", i, got)
		}
	}
}

func TestPlacementReturnsSingletonCapError(t *testing.T) {
	leader := true
	s := &Scheduler{
		isLeader:          &leader,
		logger:            log15.New(),
		jobs:              Jobs{},
		volumes:           map[string]*Volume{},
		hostVolumeIDs:     map[string]map[string]struct{}{},
		hosts:             map[string]*Host{},
		controllerPersist: make(chan interface{}, 4),
	}
	for _, id := range []string{"h1", "h2"} {
		s.hosts[id] = &Host{ID: id, Healthy: true, Tags: map[string]string{singletonCapTag: "1"}}
		job := persistSingletonTestJob("redis")
		markPlaceable(job)
		job.Formation.App.ID = id + "-app"
		job.AppID = job.Formation.App.ID
		job.ID = id
		job.HostID = id
		job.State = JobStateRunning
		s.jobs[id] = job
	}
	next := persistSingletonTestJob("postgres")
	markPlaceable(next)
	next.ID = "next"
	next.State = JobStatePending
	req := &PlacementRequest{Job: next, Err: make(chan error, 1)}
	s.HandlePlacementRequest(req)
	if err := <-req.Err; err != ErrPersistentSingletonCap {
		t.Fatalf("error %v", err)
	}
	if next.State != JobStateBlocked || next.HostID != "" {
		t.Fatalf("state=%s host=%q", next.State, next.HostID)
	}
}

func TestPlacementPinsVolumeOverSingletonCap(t *testing.T) {
	running := persistSingletonTestJob("redis")
	running.ID = "redis"
	running.HostID = "h1"
	running.State = JobStateRunning
	leader := true
	s := &Scheduler{
		isLeader: &leader,
		logger:   log15.New(),
		jobs:     Jobs{"redis": running},
		hosts: map[string]*Host{
			"h1": {ID: "h1", Healthy: true, Tags: map[string]string{singletonCapTag: "1"}},
			"h2": {ID: "h2", Healthy: true},
		},
		volumes:       map[string]*Volume{},
		hostVolumeIDs: map[string]map[string]struct{}{"h1": {"vol": {}}},
	}
	next := persistSingletonTestJob("postgres")
	markPlaceable(next)
	next.ID = "next"
	next.State = JobStatePending
	vol := &Volume{Volume: ct.Volume{
		ID:        "vol",
		HostID:    "h1",
		AppID:     next.AppID,
		ReleaseID: next.ReleaseID,
		JobType:   next.Type,
		State:     ct.VolumeStateCreated,
		VolumeReq: ct.VolumeReq{Path: "/data"},
	}}
	s.volumes[vol.ID] = vol
	req := &PlacementRequest{Job: next, Err: make(chan error, 1)}
	s.HandlePlacementRequest(req)
	if err := <-req.Err; err != nil {
		t.Fatalf("pinned volume must still place: %v", err)
	}
	if req.Host == nil || req.Host.ID != "h1" || next.HostID != "h1" {
		t.Fatalf("host %v job host %q", req.Host, next.HostID)
	}
}

func TestScaleDownSingletonWakesBlockedJob(t *testing.T) {
	s, h := singletonCapLoop(t, "1")
	running := placedSingleton("running", "redis", "h1", JobStateRunning)
	running.Formation.Processes = Processes{"redis": 0}
	blocked := placedSingleton("blocked", "postgres", "", JobStateBlocked)
	s.jobs = Jobs{"running": running, "blocked": blocked}
	done := serveOnePlacement(s)
	s.handleJobStatus(running, host.StatusDone)
	waitPlacement(t, done)
	if blocked.State == JobStateBlocked || blocked.HostID != h.ID {
		t.Fatalf("state=%s host=%q", blocked.State, blocked.HostID)
	}
}

func TestRestartLimitSingletonWakesBlockedJob(t *testing.T) {
	s, h := singletonCapLoop(t, "1")
	s.maxRestarts = 1
	s.generateJobUUID = random.UUID
	running := placedSingleton("running", "redis", "h1", JobStateRunning)
	running.Restarts = 1
	running.StartedAt = time.Now()
	running.Formation.Processes = Processes{"redis": 1}
	blocked := placedSingleton("blocked", "postgres", "", JobStateBlocked)
	s.jobs = Jobs{"running": running, "blocked": blocked}
	done := serveOnePlacement(s)
	s.handleJobStatus(running, host.StatusDone)
	waitPlacement(t, done)
	if blocked.State == JobStateBlocked || blocked.HostID != h.ID {
		t.Fatalf("state=%s host=%q", blocked.State, blocked.HostID)
	}
}

func TestRestartSingletonDoesNotWakeBlockedJob(t *testing.T) {
	s, _ := singletonCapLoop(t, "1")
	s.generateJobUUID = random.UUID
	running := placedSingleton("running", "redis", "h1", JobStateRunning)
	running.StartedAt = time.Now()
	running.Formation.Processes = Processes{"redis": 1}
	blocked := placedSingleton("blocked", "postgres", "", JobStateBlocked)
	s.jobs = Jobs{"running": running, "blocked": blocked}
	go func() {
		for req := range s.placementRequests {
			s.HandlePlacementRequest(req)
		}
	}()
	s.handleJobStatus(running, host.StatusDone)
	if blocked.State != JobStateBlocked || blocked.HostID != "" {
		t.Fatalf("replacement must not wake a waiter, state=%s host=%q", blocked.State, blocked.HostID)
	}
}

func TestWebStopDoesNotWakeSingletonWaiter(t *testing.T) {
	s, _ := singletonCapLoop(t, "1")
	web := &Job{
		ID:     "web",
		HostID: "h1",
		Type:   "web",
		State:  JobStateRunning,
		Formation: NewFormation(&ct.ExpandedFormation{
			App:       &ct.App{ID: "shop"},
			Release:   &ct.Release{ID: "rel", Processes: map[string]ct.ProcessType{"web": {}}},
			Processes: Processes{"web": 0},
		}),
	}
	web.AppID = "shop"
	web.ReleaseID = "rel"
	blocked := placedSingleton("blocked", "postgres", "", JobStateBlocked)
	s.jobs = Jobs{"web": web, "blocked": blocked}
	s.handleJobStatus(web, host.StatusDone)
	if blocked.State != JobStateBlocked {
		t.Fatalf("web stop woke a database waiter: %s", blocked.State)
	}
}

func TestCreateVolumeFailureDropsPendingVolume(t *testing.T) {
	log := log15.New()
	c1 := testutils.NewFakeHostClient("h1", false)
	c2 := testutils.NewFakeHostClient("h2", false)
	h1 := NewHost(c1, log)
	h2 := NewHost(c2, log)
	h2.Healthy = false
	leader := true
	s := &Scheduler{
		isLeader:          &leader,
		logger:            log,
		jobs:              Jobs{},
		volumes:           map[string]*Volume{},
		hostVolumeIDs:     map[string]map[string]struct{}{},
		hosts:             map[string]*Host{"h1": h1, "h2": h2},
		controllerPersist: make(chan interface{}, 8),
		placementRequests: make(chan *PlacementRequest),
	}
	go func() {
		for range s.controllerPersist {
		}
	}()
	go func() {
		for req := range s.placementRequests {
			s.HandlePlacementRequest(req)
		}
	}()
	c1.OnCreateVolume = func() error {
		h1.Healthy = false
		h2.Healthy = true
		return errors.New("zfs has no space")
	}
	job := persistSingletonTestJob("redis")
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
		t.Fatal("CreateVolume failure kept retrying the same host")
	}
	if job.HostID != "h2" {
		t.Fatalf("host %q, want h2", job.HostID)
	}
	for _, vol := range s.volumes {
		if vol.HostID == "h1" && vol.GetState() == ct.VolumeStatePending {
			t.Fatal("pending volume on the failed host was kept")
		}
	}
}

func TestAddJobFailureKeepsCreatedVolume(t *testing.T) {
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
	go func() {
		for range s.controllerPersist {
		}
	}()
	go func() {
		for req := range s.placementRequests {
			s.HandlePlacementRequest(req)
		}
	}()
	var adds int
	c.OnAddJob = func(*host.Job) {
		adds++
		if adds == 1 {
			c.AddJobErr = errors.New("busy")
			return
		}
		c.AddJobErr = nil
	}
	job := persistSingletonTestJob("redis")
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
		t.Fatal("AddJob failure did not retry")
	}
	if job.HostID != "h1" {
		t.Fatalf("host %q", job.HostID)
	}
	if len(job.Volumes) != 1 || job.Volumes[0].GetState() != ct.VolumeStateCreated || job.Volumes[0].HostID != "h1" {
		t.Fatalf("created volume was not kept: %+v", job.Volumes)
	}
	if len(s.volumes) != 1 {
		t.Fatalf("volumes %+v", s.volumes)
	}
}

func placedSingleton(id, typ, hostID string, state JobState) *Job {
	job := persistSingletonTestJob(typ)
	markPlaceable(job)
	job.Formation.App.ID = id + "-app"
	job.Formation.Release.ID = id + "-rel"
	job.AppID = job.Formation.App.ID
	job.ReleaseID = job.Formation.Release.ID
	job.ID = id
	job.HostID = hostID
	job.State = state
	if job.Formation.Processes == nil {
		job.Formation.Processes = Processes{typ: 1}
	}
	return job
}

func singletonCapLoop(t *testing.T, cap string) (*Scheduler, *Host) {
	t.Helper()
	leader := true
	hc := testutils.NewFakeHostClient("h1", false)
	h := NewHost(hc, log15.New())
	h.Tags = map[string]string{singletonCapTag: cap}
	s := &Scheduler{
		isLeader:          &leader,
		logger:            log15.New(),
		jobs:              Jobs{},
		volumes:           map[string]*Volume{},
		hostVolumeIDs:     map[string]map[string]struct{}{},
		hosts:             map[string]*Host{"h1": h},
		rectifyBatch:      map[utils.FormationKey]struct{}{},
		restartsExhausted: map[utils.FormationKey]Processes{},
		controllerPersist: make(chan interface{}, 8),
		placementRequests: make(chan *PlacementRequest),
		generateJobUUID:   random.UUID,
	}
	go func() {
		for range s.controllerPersist {
		}
	}()
	return s, h
}

func serveOnePlacement(s *Scheduler) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		req := <-s.placementRequests
		s.HandlePlacementRequest(req)
		close(done)
	}()
	return done
}

func waitPlacement(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked singleton was not placed")
	}
}
