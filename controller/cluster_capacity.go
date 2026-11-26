package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"golang.org/x/net/context"
)

// capacityHost is one host's input to buildClusterCapacity.
type capacityHost struct {
	ID       string
	Tags     map[string]string
	Shutdown bool
	Stats    *host.HostResourceStats
	StatsErr error
}

// capacityStatsTimeout bounds one host's stats read for GET /cluster/capacity.
const capacityStatsTimeout = 5 * time.Second

// buildClusterCapacity sums, per host, what each pending, starting, up or
// stopping job reserves under rules: the scheduler's own accounting
// (controller/scheduler hostReservedResources), from job_cache instead of the
// scheduler's memory. A job whose app or release cannot be found, or whose
// type the release does not define (a one-off), holds nothing, as in the
// scheduler. CPU totals are CPUCount * 1000, as the scheduler reads them.
func buildClusterCapacity(rules placement.Reservations, hosts []capacityHost, jobs []*ct.Job, lookup func(appID, releaseID string) (*ct.App, *ct.Release)) *ct.ClusterCapacity {
	out := &ct.ClusterCapacity{TenancyMode: rules.TenancyMode, ReserveProfiles: rules.ProfileReserve, Hosts: make([]*ct.HostCapacity, 0, len(hosts))}
	byID := make(map[string]*ct.HostCapacity, len(hosts))
	for _, h := range hosts {
		hc := &ct.HostCapacity{ID: h.ID, Tags: h.Tags, Shutdown: h.Shutdown}
		switch {
		case h.StatsErr != nil:
			hc.StatsError = h.StatsErr.Error()
		case h.Stats != nil:
			hc.MemoryTotalBytes = h.Stats.MemoryTotalBytes
			hc.CPUMilli = int64(h.Stats.CPUCount) * 1000
		}
		out.Hosts = append(out.Hosts, hc)
		byID[h.ID] = hc
	}
	for _, job := range jobs {
		if job == nil || !jobHoldsReservation(job) {
			continue
		}
		hc := byID[job.HostID]
		if hc == nil || lookup == nil {
			continue
		}
		app, release := lookup(job.AppID, job.ReleaseID)
		if release == nil {
			continue
		}
		proc, ok := release.Processes[job.Type]
		if !ok || !rules.Reserves(app, proc) {
			continue
		}
		mem, cpu := placement.Request(proc, true)
		if mem <= 0 && cpu <= 0 {
			continue
		}
		hc.ReservedMemoryBytes += mem
		hc.ReservedCPUMilli += cpu
		hc.Reserved = append(hc.Reserved, &ct.ReservedJob{
			JobID: job.ID, AppID: job.AppID, ReleaseID: job.ReleaseID, Type: job.Type,
			MemoryBytes: mem, CPUMilli: cpu,
		})
	}
	return out
}

// jobHoldsReservation matches the scheduler: a placed job holds its
// reservation until it is down.
func jobHoldsReservation(job *ct.Job) bool {
	if job.HostID == "" {
		return false
	}
	switch job.State {
	case ct.JobStatePending, ct.JobStateStarting, ct.JobStateUp, ct.JobStateStopping:
		return true
	default:
		return false
	}
}

// reservationRules reads the tenancy mode and the runtime profiles' stored
// flags.
func (c *controllerAPI) reservationRules() (placement.Reservations, error) {
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		return placement.Reservations{}, err
	}
	profiles, err := c.runtimeProfileRepo.List()
	if err != nil {
		return placement.Reservations{}, err
	}
	flags := make(map[string]bool, len(profiles))
	for _, p := range profiles {
		if p == nil {
			continue
		}
		flags[strings.ToLower(strings.TrimSpace(p.Name))] = p.ReserveResources
	}
	return placement.Reservations{TenancyMode: settings.Mode, ProfileReserve: flags}, nil
}

// capacityHosts reads every host's totals at once, each bounded by
// capacityStatsTimeout. A host that does not answer gets StatsErr.
func capacityHosts(hosts []utils.HostClient) []capacityHost {
	out := make([]capacityHost, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		if h == nil {
			continue
		}
		info := hostInfoFrom(h)
		out[i] = capacityHost{ID: info.ID, Tags: info.Tags, Shutdown: info.Shutdown}
		wg.Add(1)
		go func(i int, h utils.HostClient) {
			defer wg.Done()
			type result struct {
				stats *host.HostResourceStats
				err   error
			}
			done := make(chan result, 1)
			go func() {
				s, err := h.GetStats()
				done <- result{s, err}
			}()
			select {
			case r := <-done:
				out[i].Stats, out[i].StatsErr = r.stats, r.err
			case <-time.After(capacityStatsTimeout):
				out[i].StatsErr = errCapacityStatsTimeout
			}
		}(i, h)
	}
	wg.Wait()
	kept := out[:0]
	for _, h := range out {
		if h.ID != "" {
			kept = append(kept, h)
		}
	}
	return kept
}

type capacityError string

func (e capacityError) Error() string { return string(e) }

const errCapacityStatsTimeout = capacityError("host stats timed out")

// clusterCapacity is the capacity view GET /cluster/capacity returns.
func (c *controllerAPI) clusterCapacity() (*ct.ClusterCapacity, error) {
	rules, err := c.reservationRules()
	if err != nil {
		return nil, err
	}
	clients, err := c.clusterClient.Hosts()
	if err != nil {
		return nil, err
	}
	jobs, err := c.jobRepo.ListActive()
	if err != nil {
		return nil, err
	}
	apps := map[string]*ct.App{}
	releases := map[string]*ct.Release{}
	lookup := func(appID, releaseID string) (*ct.App, *ct.Release) {
		app, ok := apps[appID]
		if !ok {
			if v, err := c.appRepo.Get(appID); err == nil {
				app, _ = v.(*ct.App)
			}
			apps[appID] = app
		}
		release, ok := releases[releaseID]
		if !ok {
			if v, err := c.releaseRepo.Get(releaseID); err == nil {
				release, _ = v.(*ct.Release)
			}
			releases[releaseID] = release
		}
		return app, release
	}
	return buildClusterCapacity(rules, capacityHosts(clients), jobs, lookup), nil
}

// GetClusterCapacity is GET /cluster/capacity (cluster key or cluster admin).
func (c *controllerAPI) GetClusterCapacity(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	capacity, err := c.clusterCapacity()
	if err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, capacity)
}
