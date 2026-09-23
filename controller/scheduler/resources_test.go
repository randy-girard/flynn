package main

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func testJobWithRequest(mem, cpu int64) *Job {
	return &Job{
		Type: "web",
		Formation: &Formation{
			ExpandedFormation: &ct.ExpandedFormation{
				Release: &ct.Release{
					Processes: map[string]ct.ProcessType{
						"web": {
							Resources: resource.Resources{
								resource.TypeMemory: {Request: typeconv.Int64Ptr(mem), Limit: typeconv.Int64Ptr(mem)},
								resource.TypeCPU:    {Request: typeconv.Int64Ptr(cpu), Limit: typeconv.Int64Ptr(cpu)},
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
	s := &Scheduler{jobs: Jobs{}, hosts: map[string]*Host{}}
	h := &Host{ID: "h1"}
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
				Formation: testJobWithRequest(2<<30, 1500).Formation,
			},
		},
		hosts: map[string]*Host{},
	}
	h := &Host{ID: "h1", MemoryTotalBytes: 3 << 30, CPUMilli: 2000}
	next := testJobWithRequest(2<<30, 1000)
	if s.hostHasCapacity(h, next) {
		t.Fatal("expected overcommit reject")
	}
	small := testJobWithRequest(512<<20, 250)
	if !s.hostHasCapacity(h, small) {
		t.Fatal("expected remaining capacity")
	}
}

func TestPickHostIgnoresCapacityWhenReservationOff(t *testing.T) {
	h := &Host{ID: "h1", MemoryTotalBytes: 1 << 30, CPUMilli: 1000}
	s := &Scheduler{
		jobs: Jobs{
			"running": {
				ID:        "running",
				HostID:    "h1",
				State:     JobStateRunning,
				Type:      "web",
				Formation: testJobWithRequest(1<<30, 1000).Formation,
			},
		},
		hosts: map[string]*Host{"h1": h},
	}
	job := testJobWithRequest(1<<30, 1000)
	counts := map[string]int{"h1": 1}
	if got := s.pickHost(job, counts); got != h {
		t.Fatal("reservation off must still pack onto the only host")
	}
	s.reserveResources = true
	if got := s.pickHost(job, counts); got != nil {
		t.Fatal("reservation on must leave the job unplaced when the host is full")
	}
}

func TestPickHostUsesCapacityWhenReservationOn(t *testing.T) {
	h := &Host{ID: "h1", MemoryTotalBytes: 2 << 30, CPUMilli: 2000}
	s := &Scheduler{
		jobs:  Jobs{},
		hosts: map[string]*Host{"h1": h},
	}
	s.reserveResources = true
	job := testJobWithRequest(1<<30, 1000)
	if got := s.pickHost(job, nil); got != h {
		t.Fatal("reservation on must place when the host has remaining Request")
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
				Formation: testJobWithRequest(8<<30, 8000).Formation,
			},
		},
	}
	mem, cpu := s.hostReservedResources("h1")
	if mem != 0 || cpu != 0 {
		t.Fatalf("stopped jobs must not reserve: mem=%d cpu=%d", mem, cpu)
	}
}
