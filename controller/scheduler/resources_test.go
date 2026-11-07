package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func testJobWithRequest(mem, cpu int64) *Job {
	return testJobWithProfile("shared", mem, cpu, mem, cpu)
}

func testJobWithProfile(profile string, reqMem, reqCPU, limMem, limCPU int64) *Job {
	return &Job{
		Type: "web",
		Formation: &Formation{
			ExpandedFormation: &ct.ExpandedFormation{
				Release: &ct.Release{
					Processes: map[string]ct.ProcessType{
						"web": {
							RuntimeProfile: profile,
							Resources: resource.Resources{
								resource.TypeMemory: {Request: typeconv.Int64Ptr(reqMem), Limit: typeconv.Int64Ptr(limMem)},
								resource.TypeCPU:    {Request: typeconv.Int64Ptr(reqCPU), Limit: typeconv.Int64Ptr(limCPU)},
							},
						},
					},
				},
			},
		},
	}
}

func TestJobResourceRequest(t *testing.T) {
	j := testJobWithRequest(512<<20, 500)
	mem, cpu := jobResourceRequest(j)
	if mem != 512<<20 || cpu != 500 {
		t.Fatalf("got mem=%d cpu=%d", mem, cpu)
	}
	if m, c := jobResourceRequest(nil); m != 0 || c != 0 {
		t.Fatalf("nil job: %d %d", m, c)
	}
}

func TestHostHasCapacityUnknownHostAllows(t *testing.T) {
	s := &Scheduler{jobs: Jobs{}, hosts: map[string]*Host{}, profileReserve: map[string]bool{}}
	h := &Host{ID: "h1", Healthy: true}
	job := testJobWithRequest(1<<30, 1000)
	if !s.hostHasCapacity(h, job) {
		t.Fatal("unknown host capacity must not block placement")
	}
}

func TestHostHasCapacityRejectsOvercommit(t *testing.T) {
	s := &Scheduler{
		jobs: Jobs{
			"running": {
				ID:        "running",
				HostID:    "h1",
				State:     JobStateRunning,
				Type:      "web",
				Formation: testJobWithProfile("guaranteed", 2<<30, 1500, 2<<30, 1500).Formation,
			},
		},
		hosts:          map[string]*Host{},
		profileReserve: map[string]bool{"guaranteed": true},
	}
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 3 << 30, CPUMilli: 2000}
	next := testJobWithProfile("guaranteed", 2<<30, 1000, 2<<30, 1000)
	if s.hostHasCapacity(h, next) {
		t.Fatal("expected overcommit reject")
	}
	small := testJobWithProfile("guaranteed", 512<<20, 250, 512<<20, 250)
	if !s.hostHasCapacity(h, small) {
		t.Fatal("expected remaining capacity")
	}
}

func TestPickHostIgnoresCapacityWhenRuntimeIsShared(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	s := &Scheduler{
		jobs: Jobs{
			"running": {
				ID:        "running",
				HostID:    "h1",
				State:     JobStateRunning,
				Type:      "web",
				Formation: testJobWithProfile("shared", 1<<30, 1000, 1<<30, 1000).Formation,
			},
		},
		hosts:          map[string]*Host{"h1": h},
		profileReserve: map[string]bool{"shared": false},
	}
	job := testJobWithProfile("shared", 1<<30, 1000, 1<<30, 1000)
	counts := map[string]int{"h1": 1}
	if got := s.pickHost(job, counts); got != h {
		t.Fatal("shared runtime must still pack onto the only host")
	}
}

func TestPickHostUsesCapacityWhenRuntimeGuarantees(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 2 << 30, CPUMilli: 2000}
	s := &Scheduler{
		jobs:           Jobs{},
		hosts:          map[string]*Host{"h1": h},
		profileReserve: map[string]bool{"guaranteed": true},
	}
	job := testJobWithProfile("guaranteed", 1<<30, 1000, 1<<30, 1000)
	if got := s.pickHost(job, nil); got != h {
		t.Fatal("guaranteed runtime must place when the host has remaining Request")
	}
	full := &Scheduler{
		jobs: Jobs{
			"running": {
				ID:        "running",
				HostID:    "h1",
				State:     JobStateRunning,
				Type:      "web",
				Formation: testJobWithProfile("guaranteed", 2<<30, 2000, 2<<30, 2000).Formation,
			},
		},
		hosts:          map[string]*Host{"h1": h},
		profileReserve: map[string]bool{"guaranteed": true},
	}
	if got := full.pickHost(job, map[string]int{"h1": 1}); got != nil {
		t.Fatal("guaranteed runtime must leave the job unplaced when the host is full")
	}
}

func TestSharedJobsDoNotConsumeReservedCapacity(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	s := &Scheduler{
		jobs: Jobs{
			"shared": {
				ID:        "shared",
				HostID:    "h1",
				State:     JobStateRunning,
				Type:      "web",
				Formation: testJobWithProfile("shared", 0, 0, 1<<30, 1000).Formation,
			},
		},
		hosts:          map[string]*Host{"h1": h},
		profileReserve: map[string]bool{"shared": false, "guaranteed": true},
	}
	job := testJobWithProfile("guaranteed", 1<<30, 1000, 1<<30, 1000)
	if !s.hostHasCapacity(h, job) {
		t.Fatal("shared runtimes must not take guaranteed slots")
	}
}

func persistSingletonTestJob(typ string) *Job {
	return &Job{
		Type: typ,
		Formation: &Formation{
			OriginalProcesses: Processes{typ: 1},
			ExpandedFormation: &ct.ExpandedFormation{
				Release: &ct.Release{
					Processes: map[string]ct.ProcessType{
						typ: {Volumes: []ct.VolumeReq{{Path: "/data"}}},
					},
				},
			},
		},
	}
}

func TestPickHostPacksPersistentSingletons(t *testing.T) {
	h1 := &Host{ID: "h1", Healthy: true}
	h2 := &Host{ID: "h2", Healthy: true}
	h3 := &Host{ID: "h3", Healthy: true}
	redisJob := persistSingletonTestJob("redis")
	redisJob.ID = "redis-1"
	redisJob.HostID = "h1"
	redisJob.State = JobStateRunning
	s := &Scheduler{
		jobs:  Jobs{"redis-1": redisJob},
		hosts: map[string]*Host{"h1": h1, "h2": h2, "h3": h3},
	}
	kafka := persistSingletonTestJob("kafka")
	for i := 0; i < 20; i++ {
		got := s.pickHost(kafka, map[string]int{})
		if got == nil || got.ID != "h1" {
			t.Fatalf("attempt %d: packed persistent singleton onto %v, want h1 (redis volume host)", i, got)
		}
	}
}

func TestPickHostSpreadsPersistentHA(t *testing.T) {
	h1 := &Host{ID: "h1", Healthy: true}
	h2 := &Host{ID: "h2", Healthy: true}
	h3 := &Host{ID: "h3", Healthy: true}
	peer := persistSingletonTestJob("postgres")
	peer.Formation.OriginalProcesses["postgres"] = 3
	peer.ID = "pg-1"
	peer.HostID = "h1"
	peer.State = JobStateRunning
	s := &Scheduler{
		jobs:  Jobs{"pg-1": peer},
		hosts: map[string]*Host{"h1": h1, "h2": h2, "h3": h3},
	}
	next := persistSingletonTestJob("postgres")
	next.Formation.OriginalProcesses["postgres"] = 3
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, map[string]int{"h1": 1})
		if got == nil {
			t.Fatal("HA postgres peer must still place")
		}
		if got.ID == "h1" {
			t.Fatalf("attempt %d: HA peer packed onto h1; replicas must spread", i)
		}
	}
}

func TestPickHostSpreadsHAAcrossReleases(t *testing.T) {
	h1 := &Host{ID: "h1", Healthy: true}
	h2 := &Host{ID: "h2", Healthy: true}
	h3 := &Host{ID: "h3", Healthy: true}
	old := persistSingletonTestJob("postgres")
	old.AppID = "pg"
	old.Formation.OriginalProcesses["postgres"] = 3
	old.ID = "pg-old"
	old.HostID = "h1"
	old.State = JobStateRunning
	s := &Scheduler{
		jobs:  Jobs{"pg-old": old},
		hosts: map[string]*Host{"h1": h1, "h2": h2, "h3": h3},
	}
	next := persistSingletonTestJob("postgres")
	next.AppID = "pg"
	next.Formation.OriginalProcesses["postgres"] = 3
	counts := s.jobs.GetHostJobCountsByAppType("pg", "postgres")
	for i := 0; i < 20; i++ {
		got := s.pickHost(next, counts)
		if got == nil {
			t.Fatal("HA postgres peer must still place")
		}
		if got.ID == "h1" {
			t.Fatalf("attempt %d: packed onto old-release host h1", i)
		}
	}
}

func TestHostReservedIgnoresStoppedJobs(t *testing.T) {
	s := &Scheduler{
		jobs: Jobs{
			"stopped": {
				ID:        "stopped",
				HostID:    "h1",
				State:     JobStateStopped,
				Type:      "web",
				Formation: testJobWithProfile("guaranteed", 8<<30, 8000, 8<<30, 8000).Formation,
			},
		},
		profileReserve: map[string]bool{"guaranteed": true},
	}
	mem, cpu := s.hostReservedResources("h1")
	if mem != 0 || cpu != 0 {
		t.Fatalf("stopped jobs must not reserve: mem=%d cpu=%d", mem, cpu)
	}
}

func TestPickHostSkipsUnhealthyHost(t *testing.T) {
	dead := &Host{ID: "dead", Healthy: false}
	live := &Host{ID: "live", Healthy: true}
	job := testJobWithRequest(1<<20, 100)
	s := &Scheduler{
		jobs:  Jobs{},
		hosts: map[string]*Host{"dead": dead, "live": live},
	}
	for i := 0; i < 20; i++ {
		got := s.pickHost(job, map[string]int{})
		if got == nil || got.ID != "live" {
			t.Fatalf("attempt %d: placed on %v, want live (dead host crashed)", i, got)
		}
	}
	if hostCanPlace(dead) {
		t.Fatal("crashed host must not be schedulable")
	}
}

func TestPickHostAvoidsListedHost(t *testing.T) {
	h1 := &Host{ID: "h1", Healthy: true}
	h2 := &Host{ID: "h2", Healthy: true}
	job := persistSingletonTestJob("redis")
	job.Formation.Tags = map[string]map[string]string{
		"redis": {ct.FormationAvoidHostIDsTag: "h1"},
	}
	s := &Scheduler{
		jobs:  Jobs{},
		hosts: map[string]*Host{"h1": h1, "h2": h2},
	}
	for i := 0; i < 20; i++ {
		got := s.pickHost(job, map[string]int{})
		if got == nil || got.ID != "h2" {
			t.Fatalf("attempt %d: placed on %v, want h2 (avoid h1)", i, got)
		}
	}
}

// A rebooting host is only marked unhealthy, so its stopped omni job gets a
// restart. That restart must not land on a live host that already runs one:
// the doubled flannel there was stopped as surplus and took the live host's
// original down with it.
func TestPickHostSkipsHostsRunningTheirOmniShare(t *testing.T) {
	formation := NewFormation(&ct.ExpandedFormation{
		App:       &ct.App{ID: "flannel"},
		Release:   &ct.Release{ID: "r1", Processes: map[string]ct.ProcessType{"app": {Omni: true}}},
		Processes: Processes{"app": 1},
	})
	formation.RectifyOmni(3)
	running := func(id, hostID string) *Job {
		return &Job{ID: id, HostID: hostID, Type: "app", State: JobStateRunning, Formation: formation}
	}
	h1 := &Host{ID: "h1", Healthy: true}
	h2 := &Host{ID: "h2", Healthy: false}
	h3 := &Host{ID: "h3", Healthy: true}
	s := &Scheduler{
		jobs:  Jobs{"j1": running("j1", "h1"), "j3": running("j3", "h3")},
		hosts: map[string]*Host{"h1": h1, "h2": h2, "h3": h3},
	}
	restart := &Job{ID: "restart", Type: "app", State: JobStatePending, Formation: formation}
	counts := s.jobs.GetHostJobCounts(formation.key(), "app")
	for i := 0; i < 20; i++ {
		if got := s.pickHost(restart, counts); got != nil {
			t.Fatalf("attempt %d: placed omni restart on %s, which already runs one", i, got.ID)
		}
	}

	h2.Healthy = true
	for i := 0; i < 20; i++ {
		if got := s.pickHost(restart, counts); got != h2 {
			t.Fatalf("attempt %d: placed omni job on %v, want h2 (the only host without one)", i, got)
		}
	}
}

func TestPickHostHonorsMaxProcesses(t *testing.T) {
	formation := NewFormation(&ct.ExpandedFormation{
		App:       &ct.App{ID: "shop"},
		Release:   &ct.Release{Processes: map[string]ct.ProcessType{"web": {}}},
		Processes: Processes{"web": 1},
		Tags:      map[string]map[string]string{"web": {"role": "database"}},
	})
	full := &Host{ID: "db1", Healthy: true, Tags: map[string]string{"role": "database", ct.FormationMaxProcessesTag: "1"}}
	free := &Host{ID: "db2", Healthy: true, Tags: map[string]string{"role": "database"}}
	s := &Scheduler{
		jobs:  Jobs{"j1": {ID: "j1", HostID: "db1", Type: "web", State: JobStateRunning, Formation: formation}},
		hosts: map[string]*Host{"db1": full, "db2": free},
	}
	next := &Job{Type: "web", State: JobStatePending, Formation: formation}
	for i := 0; i < 10; i++ {
		if got := s.pickHost(next, map[string]int{"db1": 1}); got != free {
			t.Fatalf("attempt %d: placed on %v, want db2 (db1 is at max_processes=1)", i, got)
		}
	}
}

func markPlaceable(job *Job) {
	if job.Formation.App == nil {
		job.Formation.App = &ct.App{ID: "app"}
	}
	if job.Formation.Release.ID == "" {
		job.Formation.Release.ID = "rel"
	}
	job.AppID = job.Formation.App.ID
	job.ReleaseID = job.Formation.Release.ID
}

func newCapacityScheduler(mode string, reserve map[string]bool, host *Host, jobs Jobs) *Scheduler {
	leader := true
	if jobs == nil {
		jobs = Jobs{}
	}
	if reserve == nil {
		reserve = map[string]bool{}
	}
	s := &Scheduler{
		isLeader:          &leader,
		logger:            log15.New(),
		jobs:              jobs,
		hosts:             map[string]*Host{host.ID: host},
		profileReserve:    reserve,
		tenancyMode:       mode,
		controllerPersist: make(chan interface{}, 16),
	}
	go func() {
		for range s.controllerPersist {
		}
	}()
	return s
}

func runningReserved(profile string, mem, cpu int64) *Job {
	job := testJobWithProfile(profile, mem, cpu, mem, cpu)
	job.ID = "running"
	job.HostID = "h1"
	job.State = JobStateRunning
	markPlaceable(job)
	return job
}

func TestHostedBuiltinReservesWhenFlagOff(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	running := runningReserved("small", 1<<30, 1000)
	s := newCapacityScheduler("hosted", map[string]bool{"small": false}, h, Jobs{"running": running})
	next := testJobWithProfile("Small", 1<<30, 1000, 1<<30, 1000)
	if !s.jobReservesResources(next) {
		t.Fatal("hosted small must reserve when the stored flag is off")
	}
	if got := s.pickHost(next, map[string]int{"h1": 1}); got != nil {
		t.Fatal("second hosted small job must stay unplaced")
	}
}

func TestHostedBuiltinLimitFallbackBlocksSecondJob(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	running := testJobWithProfile("small", 0, 0, 1<<30, 1000)
	running.ID = "running"
	running.HostID = "h1"
	running.State = JobStateRunning
	markPlaceable(running)
	s := newCapacityScheduler("hosted", map[string]bool{"small": false}, h, Jobs{"running": running})
	next := testJobWithProfile("small", 0, 0, 1<<30, 1000)
	markPlaceable(next)
	next.ID = "next"
	next.State = JobStatePending
	req := &PlacementRequest{Job: next, Err: make(chan error, 1)}
	s.HandlePlacementRequest(req)
	if err := <-req.Err; err != ErrNoHostCapacity {
		t.Fatalf("error %v", err)
	}
	if next.State != JobStateBlocked || next.HostID != "" {
		t.Fatalf("state=%s host=%q", next.State, next.HostID)
	}
}

func TestSelfHostedFlagOffStillPlaces(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	running := runningReserved("small", 1<<30, 1000)
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": false}, h, Jobs{"running": running})
	next := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	if s.jobReservesResources(next) {
		t.Fatal("self-hosted small must not reserve when the flag is off")
	}
	if got := s.pickHost(next, map[string]int{"h1": 1}); got != h {
		t.Fatalf("placed on %v, want h1", got)
	}
}

func TestSelfHostedFlagOnBlocksSecondJob(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	running := runningReserved("large", 1<<30, 1000)
	running.Formation.App = &ct.App{ID: "other"}
	running.AppID = "other"
	s := newCapacityScheduler("self_hosted", map[string]bool{"large": true}, h, Jobs{"running": running})
	next := testJobWithProfile("large", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(next)
	next.ID = "next"
	next.State = JobStatePending
	s.jobs.Add(next)
	req := &PlacementRequest{Job: next, Err: make(chan error, 1)}
	s.HandlePlacementRequest(req)
	if err := <-req.Err; err != ErrNoHostCapacity {
		t.Fatalf("error %v", err)
	}
	if next.State != JobStateBlocked || next.HostID != "" {
		t.Fatalf("state=%s host=%q", next.State, next.HostID)
	}
	if got := s.jobs.GetProcesses(next.Formation.key())["web"]; got != 1 {
		t.Fatalf("formation count %d", got)
	}
	if mem, cpu := s.hostReservedResources("h1"); mem != 1<<30 || cpu != 1000 {
		t.Fatalf("blocked job changed the reserved sum: mem=%d cpu=%d", mem, cpu)
	}
}

func TestHostedCustomProfileFlagOffStillPlaces(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	s := newCapacityScheduler("hosted", map[string]bool{"xlarge": false}, h, nil)
	next := testJobWithProfile("xlarge", 8<<30, 8000, 8<<30, 8000)
	if s.jobReservesResources(next) {
		t.Fatal("hosted must not force a custom profile")
	}
	if got := s.pickHost(next, nil); got != h {
		t.Fatalf("placed on %v, want h1", got)
	}
}

func TestEmptyProfileDoesNotReserveWhenHosted(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	s := newCapacityScheduler("hosted", map[string]bool{}, h, nil)
	next := testJobWithProfile("", 8<<30, 8000, 8<<30, 8000)
	if s.jobReservesResources(next) {
		t.Fatal("empty runtime profile must not reserve")
	}
	if got := s.pickHost(next, nil); got != h {
		t.Fatalf("placed on %v, want h1", got)
	}
}

func TestStartJobDoesNotRetryWhenBlocked(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	running := runningReserved("small", 1<<30, 1000)
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, Jobs{"running": running})
	s.placementRequests = make(chan *PlacementRequest)
	var attempts atomic.Int32
	go func() {
		for req := range s.placementRequests {
			attempts.Add(1)
			s.HandlePlacementRequest(req)
		}
	}()
	defer close(s.placementRequests)
	next := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(next)
	next.ID = "next"
	next.State = JobStatePending
	s.StartJob(next)
	if attempts.Load() != 1 {
		t.Fatalf("placement attempts %d", attempts.Load())
	}
	if next.State != JobStateBlocked || next.HostID != "" {
		t.Fatalf("state=%s host=%q", next.State, next.HostID)
	}
}

func TestPlacementKeepsPinnedVolumeWhenHostIsFull(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	running := runningReserved("small", 1<<30, 1000)
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, Jobs{"running": running})
	s.volumes = map[string]*Volume{}
	s.hostVolumeIDs = map[string]map[string]struct{}{"h1": {"vol": {}}}
	next := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(next)
	proc := next.Formation.Release.Processes["web"]
	proc.Volumes = []ct.VolumeReq{{Path: "/data"}}
	next.Formation.Release.Processes["web"] = proc
	next.ID = "next"
	next.State = JobStatePending
	vol := &Volume{Volume: ct.Volume{
		ID:        "vol",
		HostID:    "h1",
		AppID:     next.AppID,
		ReleaseID: next.ReleaseID,
		JobType:   "web",
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

func TestMaybeStartBlockedJobWhenCapacityFrees(t *testing.T) {
	h := NewHost(testutils.NewFakeHostClient("h1", false), log15.New())
	h.Healthy = true
	h.MemoryTotalBytes = 1 << 30
	h.CPUMilli = 1000
	running := runningReserved("small", 1<<30, 1000)
	blocked := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(blocked)
	blocked.ID = "blocked"
	blocked.State = JobStateBlocked
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, Jobs{"running": running, "blocked": blocked})
	s.placementRequests = make(chan *PlacementRequest)
	done := make(chan struct{})
	go func() {
		req := <-s.placementRequests
		s.HandlePlacementRequest(req)
		close(done)
	}()
	running.State = JobStateStopped
	s.maybeStartBlockedJobs(h)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("capacity-blocked job was not retried")
	}
	if blocked.State == JobStateBlocked || blocked.HostID != "h1" {
		t.Fatalf("state=%s host=%q", blocked.State, blocked.HostID)
	}
}

func TestSyncTenancyKeepsLastModeOnError(t *testing.T) {
	fake := testutils.NewFakeControllerClient()
	fake.Tenancy = &ct.TenancySettings{Mode: "  hosted  "}
	s := &Scheduler{ControllerClient: fake}
	s.syncTenancy(log15.New())
	if s.tenancyMode != "hosted" {
		t.Fatalf("mode %q", s.tenancyMode)
	}
	fake.TenancyErr = errors.New("down")
	s.syncTenancy(log15.New())
	if s.tenancyMode != "hosted" {
		t.Fatalf("error cleared mode %q", s.tenancyMode)
	}
	fake.TenancyErr = nil
	fake.Tenancy = &ct.TenancySettings{Mode: "self_hosted"}
	s.syncTenancy(log15.New())
	if s.tenancyMode != "self_hosted" {
		t.Fatalf("mode %q", s.tenancyMode)
	}
}

func TestSyncTenancyErrorBeforeFirstReadStaysEmpty(t *testing.T) {
	fake := testutils.NewFakeControllerClient()
	fake.TenancyErr = errors.New("down")
	s := &Scheduler{ControllerClient: fake}
	s.syncTenancy(log15.New())
	if s.tenancyMode != "" {
		t.Fatalf("mode %q", s.tenancyMode)
	}
	if s.jobReservesResources(testJobWithProfile("small", 1, 1, 1, 1)) {
		t.Fatal("empty tenancy must not reserve a builtin")
	}
}
