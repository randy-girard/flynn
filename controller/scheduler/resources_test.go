package main

import (
	"testing"

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
