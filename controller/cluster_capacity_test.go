package main

import (
	"errors"
	"testing"

	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func capacityRelease(id string, procs map[string]string) *ct.Release {
	r := &ct.Release{ID: id, Processes: map[string]ct.ProcessType{}}
	for typ, profile := range procs {
		// Release.Processes as data/release.go stores a non-reserving
		// runtime: Request 0, Limit the runtime size.
		r.Processes[typ] = ct.ProcessType{RuntimeProfile: profile, Resources: resource.Resources{
			resource.TypeMemory: {Request: typeconv.Int64Ptr(0), Limit: typeconv.Int64Ptr(512 << 20)},
			resource.TypeCPU:    {Request: typeconv.Int64Ptr(0), Limit: typeconv.Int64Ptr(500)},
		}}
	}
	return r
}

// TestBuildClusterCapacity: the capacity view counts what the scheduler
// counts. A hosted tenant job on small reserves at its limit, a platform
// job (router) does not reserve, and a down job and a one-off type the
// release does not define hold nothing. A host whose stats failed reports
// the error and zero totals.
func TestBuildClusterCapacity(t *testing.T) {
	tenant := &ct.App{ID: "a-shop", Name: "shop"}
	router := &ct.App{ID: "a-router", Name: "router", Meta: map[string]string{"flynn-system-app": "true"}}
	releases := map[string]*ct.Release{
		"r-shop":   capacityRelease("r-shop", map[string]string{"web": "small"}),
		"r-router": capacityRelease("r-router", map[string]string{"app": "small"}),
	}
	apps := map[string]*ct.App{tenant.ID: tenant, router.ID: router}
	lookup := func(appID, releaseID string) (*ct.App, *ct.Release) {
		return apps[appID], releases[releaseID]
	}
	jobs := []*ct.Job{
		{ID: "h1-web1", HostID: "h1", AppID: tenant.ID, ReleaseID: "r-shop", Type: "web", State: ct.JobStateUp},
		{ID: "h1-web2", HostID: "h1", AppID: tenant.ID, ReleaseID: "r-shop", Type: "web", State: ct.JobStateStopping},
		{ID: "h1-down", HostID: "h1", AppID: tenant.ID, ReleaseID: "r-shop", Type: "web", State: ct.JobStateDown},
		{ID: "h1-run", HostID: "h1", AppID: tenant.ID, ReleaseID: "r-shop", Type: "run", State: ct.JobStateUp},
		{ID: "h1-router", HostID: "h1", AppID: router.ID, ReleaseID: "r-router", Type: "app", State: ct.JobStateUp},
	}
	hosts := []capacityHost{
		{ID: "h1", Tags: map[string]string{"k": "v"}, Stats: &host.HostResourceStats{MemoryTotalBytes: 4 << 30, CPUCount: 2}},
		{ID: "h2", StatsErr: errors.New("timeout")},
	}
	rules := placement.Reservations{TenancyMode: placement.TenancyHosted, ProfileReserve: map[string]bool{"small": false}}
	got := buildClusterCapacity(rules, hosts, jobs, lookup)
	if got.TenancyMode != "hosted" || len(got.Hosts) != 2 {
		t.Fatalf("capacity %+v", got)
	}
	h1 := got.Hosts[0]
	if h1.ID != "h1" || h1.MemoryTotalBytes != 4<<30 || h1.CPUMilli != 2000 || h1.Tags["k"] != "v" {
		t.Fatalf("h1 %+v", h1)
	}
	if h1.ReservedMemoryBytes != 2*(512<<20) || h1.ReservedCPUMilli != 1000 || len(h1.Reserved) != 2 {
		t.Fatalf("h1 reserved %d %d %d: want the up and the stopping tenant job only", h1.ReservedMemoryBytes, h1.ReservedCPUMilli, len(h1.Reserved))
	}
	for _, r := range h1.Reserved {
		if r.AppID != tenant.ID || r.MemoryBytes != 512<<20 || r.CPUMilli != 500 {
			t.Fatalf("reserved %+v", r)
		}
	}
	h2 := got.Hosts[1]
	if h2.StatsError != "timeout" || h2.MemoryTotalBytes != 0 || h2.CPUMilli != 0 {
		t.Fatalf("h2 %+v", h2)
	}

	self := buildClusterCapacity(placement.Reservations{TenancyMode: "self_hosted"}, hosts, jobs, lookup)
	if self.Hosts[0].ReservedMemoryBytes != 0 || len(self.Hosts[0].Reserved) != 0 {
		t.Fatalf("self_hosted small with the flag off reserves nothing: %+v", self.Hosts[0])
	}
}
