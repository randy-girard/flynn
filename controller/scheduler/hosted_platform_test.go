package main

import (
	"fmt"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

const (
	smallMem = 512 << 20
	smallCPU = 500
)

// hostedJob is a job of app on the builtin small profile (512 MB, 500m CPU,
// the hosted default) with the request copied onto the process.
func hostedJob(id string, app *ct.App, typ string, omni bool) *Job {
	job := &Job{
		ID:   id,
		Type: typ,
		Formation: &Formation{
			ExpandedFormation: &ct.ExpandedFormation{
				App: app,
				Release: &ct.Release{
					ID: "rel-" + app.Name,
					Processes: map[string]ct.ProcessType{
						typ: {
							RuntimeProfile: resource.ProfileSmall,
							Omni:           omni,
							Resources: resource.Resources{
								resource.TypeMemory: {Request: typeconv.Int64Ptr(smallMem), Limit: typeconv.Int64Ptr(smallMem)},
								resource.TypeCPU:    {Request: typeconv.Int64Ptr(smallCPU), Limit: typeconv.Int64Ptr(smallCPU)},
							},
						},
					},
				},
			},
			OriginalProcesses: map[string]int{typ: 1},
		},
	}
	job.AppID = app.ID
	job.ReleaseID = job.Formation.Release.ID
	return job
}

func systemApp(name string, extra ...string) *ct.App {
	meta := map[string]string{"flynn-system-app": "true"}
	for i := 0; i+1 < len(extra); i += 2 {
		meta[extra[i]] = extra[i+1]
	}
	return &ct.App{ID: "id-" + name, Name: name, Meta: meta}
}

// overReservedHostedScheduler is the NEW-45 dev cluster shape: hosted mode, a
// 2-CPU 4 GB host, and 65 system jobs on the builtin small profile. Before
// NEW-45 those 65 jobs reserved 32.5 GB and 32.5 CPU on the host, so no new
// job of any app could be placed.
func overReservedHostedScheduler(t *testing.T) *Scheduler {
	t.Helper()
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 4 << 30, CPUMilli: 2000}
	platform := []*ct.App{
		systemApp("controller"),
		systemApp("router"),
		systemApp("blobstore"),
		systemApp("discoverd"),
		systemApp("postgres", "flynn-datastore", "true"),
		systemApp("redis-plugin", "flynn-plugin", "true", "flynn-datastore", "true"),
		systemApp("dashboard", "flynn-plugin", "true"),
	}
	jobs := Jobs{}
	for i := 0; i < 65; i++ {
		app := platform[i%len(platform)]
		id := fmt.Sprintf("sys-%d", i)
		job := hostedJob(id, app, "web", false)
		job.HostID = "h1"
		job.State = JobStateRunning
		jobs[id] = job
	}
	s := newCapacityScheduler("hosted", map[string]bool{resource.ProfileSmall: false}, h, jobs)
	if used, _ := s.hostReservedResources("h1"); used != 0 {
		t.Errorf("platform jobs reserve %d bytes on a hosted host; they must not count against tenant reservations", used)
	}
	return s
}

// TestHostedPlatformJobsAreNotBlockedByReservations: NEW-45. A replacement
// router, controller, plugin app or platform postgres job is placed on a
// hosted host whatever the reservation sum, because system and platform jobs
// neither count against nor are blocked by tenant reservations.
func TestHostedPlatformJobsAreNotBlockedByReservations(t *testing.T) {
	s := overReservedHostedScheduler(t)
	for _, tc := range []struct {
		app  *ct.App
		typ  string
		omni bool
	}{
		{systemApp("router"), "app", true},
		{systemApp("controller"), "web", false},
		{systemApp("controller"), "scheduler", true},
		{systemApp("blobstore"), "web", false},
		{systemApp("postgres", "flynn-datastore", "true"), "postgres", false},
		{systemApp("redis-plugin", "flynn-plugin", "true", "flynn-datastore", "true"), "web", false},
	} {
		job := hostedJob("new-"+tc.app.Name+"-"+tc.typ, tc.app, tc.typ, tc.omni)
		if s.jobReservesResources(job) {
			t.Errorf("%s %s reserves in hosted mode", tc.app.Name, tc.typ)
		}
		if got := s.pickHost(job, map[string]int{}); got == nil {
			t.Errorf("%s %s was not placed on the over-reserved hosted host", tc.app.Name, tc.typ)
		}
	}
}

// TestHostedTenantJobsStillReserve: tenant apps and tenant datastores
// (redis-<uuid>, isolated postgres instances) keep reserving in hosted mode.
// System jobs do not use up their capacity, and the tenant total still
// blocks the job that does not fit.
func TestHostedTenantJobsStillReserve(t *testing.T) {
	s := overReservedHostedScheduler(t)
	tenant := &ct.App{ID: "id-shop", Name: "shop", OwnerAccount: "user:a"}
	redis := systemApp("redis-11111111-1111-1111-1111-111111111111", "flynn-datastore", "true")
	first := hostedJob("tenant-0", tenant, "web", false)
	if !s.jobReservesResources(first) {
		t.Fatal("a tenant job on small must reserve in hosted mode")
	}
	if !s.jobReservesResources(hostedJob("redis", redis, "redis", false)) {
		t.Fatal("a tenant redis appliance must reserve in hosted mode")
	}
	if s.pickHost(first, map[string]int{}) == nil {
		t.Fatal("the first tenant job must fit: platform jobs do not use tenant capacity")
	}
	// 4 GB / 512 MB and 2000m / 500m: four tenant jobs fit.
	for i := 0; i < 4; i++ {
		job := hostedJob(fmt.Sprintf("tenant-%d", i), tenant, "web", false)
		job.HostID = "h1"
		job.State = JobStateRunning
		s.jobs[job.ID] = job
	}
	if s.pickHost(hostedJob("tenant-next", tenant, "web", false), map[string]int{}) != nil {
		t.Fatal("a fifth tenant job must stay blocked: tenant reservations are full")
	}
	if s.pickHost(hostedJob("router-next", systemApp("router"), "app", true), map[string]int{}) == nil {
		t.Fatal("the router must still be placed while tenant reservations are full")
	}
}

// TestSelfHostedReserveFlagStillAppliesToSystemJobs: the exemption is hosted
// only. In self_hosted mode an operator who turns reserve_resources on for a
// profile still reserves it for every job on that profile.
func TestSelfHostedReserveFlagStillAppliesToSystemJobs(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 4 << 30, CPUMilli: 2000}
	s := newCapacityScheduler("self_hosted", map[string]bool{resource.ProfileSmall: true}, h, Jobs{})
	if !s.jobReservesResources(hostedJob("c", systemApp("controller"), "web", false)) {
		t.Fatal("self_hosted reserve_resources must still reserve a system job")
	}
}
