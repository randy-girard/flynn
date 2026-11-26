package deployment

import (
	"fmt"
	"sort"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
)

// capacityReader is GET /cluster/capacity. The v1 controller client has it;
// a client without it skips the pre-check.
type capacityReader interface {
	GetClusterCapacity() (*ct.ClusterCapacity, error)
}

// stopFirstTypes are the process types this deploy rolls by stopping an old
// job before its replacement can start: every type of a one-down-one-up
// deploy, and the omni types of a one-by-one deploy (omniRollStep stops the
// old job on a host, then starts the new one there; a host-network process
// such as the router cannot do it the other way round on one host). Every
// other roll starts the new job first, so a replacement that cannot be placed
// fails the deploy with the old jobs still running.
func (d *DeployJob) stopFirstTypes() []string {
	var out []string
	for typ, n := range d.Processes {
		if n <= 0 || d.newRelease == nil {
			continue
		}
		if _, ok := d.newRelease.Processes[typ]; !ok {
			continue
		}
		switch d.Strategy {
		case "one-down-one-up":
			out = append(out, typ)
		case "one-by-one":
			if d.processIsOmni(typ) {
				out = append(out, typ)
			}
		}
	}
	sort.Strings(out)
	return out
}

// capacityPreCheckError is returned before anything was stopped, so there is
// nothing to roll back.
func capacityPreCheckError(format string, args ...interface{}) error {
	return ErrSkipRollback{Err: fmt.Sprintf(format, args...) + "; no job was stopped. Free reserved capacity on that host (or give it more CPU and memory), then deploy again"}
}

type hostRoom struct {
	h          *ct.HostCapacity
	usedMem    int64
	usedCPU    int64
	unreadable bool
}

func (r *hostRoom) fits(mem, cpu int64) bool {
	return placement.Fits(r.h.MemoryTotalBytes, r.h.CPUMilli, r.usedMem, r.usedCPU, mem, cpu)
}

// checkCapacityBeforeStop runs before a roll stops anything (NEW-45). For
// each stop-first type whose new process reserves CPU and memory (the
// scheduler's rule, placement.Reservations), it checks with
// GET /cluster/capacity that the replacement can be placed once the old job
// stops: on the same host for an omni process, on any host for the others.
// If not, the deploy fails with a clear message and no job stopped. A
// platform process in hosted mode never reserves, so the router and the
// controller always pass. When capacity cannot be read (an older controller,
// a host whose totals failed) the check does not block: it refuses only on
// proof that the replacement cannot be placed.
func (d *DeployJob) checkCapacityBeforeStop(log log15.Logger) error {
	types := d.stopFirstTypes()
	if len(types) == 0 {
		return nil
	}
	reader, ok := d.client.(capacityReader)
	if !ok {
		return nil
	}
	app, err := d.client.GetApp(d.AppID)
	if err != nil {
		log.Warn("capacity pre-check skipped: cannot read the app", "err", err)
		return nil
	}
	capacity, err := reader.GetClusterCapacity()
	if err != nil || capacity == nil {
		log.Warn("capacity pre-check skipped: cannot read cluster capacity", "err", err)
		return nil
	}
	rules := placement.Reservations{TenancyMode: capacity.TenancyMode, ProfileReserve: capacity.ReserveProfiles}
	var jobs []*ct.Job
	for _, typ := range types {
		proc := d.newRelease.Processes[typ]
		if !rules.Reserves(app, proc) {
			continue
		}
		needMem, needCPU := placement.Request(proc, true)
		if needMem <= 0 && needCPU <= 0 {
			continue
		}
		if jobs == nil {
			if jobs, err = d.client.JobList(d.AppID); err != nil {
				log.Warn("capacity pre-check skipped: cannot list jobs", "err", err)
				return nil
			}
		}
		rooms, order := roomsFrom(capacity)
		old := oldJobsOf(jobs, d.OldReleaseID, typ)
		if d.processIsOmni(typ) {
			if err := checkOmniRoll(typ, rooms, old, needMem, needCPU); err != nil {
				return err
			}
			continue
		}
		if err := checkOneDownOneUp(typ, rooms, order, old, d.Processes[typ], needMem, needCPU); err != nil {
			return err
		}
	}
	return nil
}

func roomsFrom(capacity *ct.ClusterCapacity) (map[string]*hostRoom, []string) {
	rooms := make(map[string]*hostRoom, len(capacity.Hosts))
	var order []string
	for _, h := range capacity.Hosts {
		if h == nil || h.Shutdown {
			continue
		}
		rooms[h.ID] = &hostRoom{
			h: h, usedMem: h.ReservedMemoryBytes, usedCPU: h.ReservedCPUMilli,
			unreadable: h.StatsError != "" || (h.MemoryTotalBytes == 0 && h.CPUMilli == 0),
		}
		order = append(order, h.ID)
	}
	sort.Strings(order)
	return rooms, order
}

func oldJobsOf(jobs []*ct.Job, releaseID, typ string) []*ct.Job {
	var out []*ct.Job
	for _, j := range jobs {
		if j != nil && j.ReleaseID == releaseID && j.Type == typ && j.HostID != "" && jobHoldsHostPort(j) {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].HostID < out[b].HostID })
	return out
}

// free takes the old job's reservation off its host, if it holds one.
func (r *hostRoom) free(job *ct.Job) {
	for _, res := range r.h.Reserved {
		if res == nil {
			continue
		}
		if (job.ID != "" && res.JobID == job.ID) || (job.UUID != "" && res.JobID == job.UUID) {
			r.usedMem -= res.MemoryBytes
			r.usedCPU -= res.CPUMilli
			return
		}
	}
}

func checkOmniRoll(typ string, rooms map[string]*hostRoom, old []*ct.Job, needMem, needCPU int64) error {
	for _, j := range old {
		room := rooms[j.HostID]
		if room == nil || room.unreadable {
			continue
		}
		room.free(j)
		if !room.fits(needMem, needCPU) {
			return capacityPreCheckError(
				"refusing to roll %s: on host %s the new job needs %d MiB and %dm CPU, but after the old job stops %d MiB and %dm of the host's %d MiB and %dm stay reserved by other jobs",
				typ, j.HostID, needMem>>20, needCPU, room.usedMem>>20, room.usedCPU, room.h.MemoryTotalBytes>>20, room.h.CPUMilli)
		}
	}
	return nil
}

// checkOneDownOneUp replays the roll: stop one old job, place one new job on
// the first host it fits, for each old job the deploy replaces.
func checkOneDownOneUp(typ string, rooms map[string]*hostRoom, order []string, old []*ct.Job, target int, needMem, needCPU int64) error {
	unreadable := false
	for _, id := range order {
		if rooms[id].unreadable {
			unreadable = true
		}
	}
	for i, j := range old {
		if i >= target {
			break
		}
		if room := rooms[j.HostID]; room != nil {
			room.free(j)
		}
		placed := false
		for _, id := range order {
			room := rooms[id]
			if room.unreadable || !room.fits(needMem, needCPU) {
				continue
			}
			room.usedMem += needMem
			room.usedCPU += needCPU
			placed = true
			break
		}
		if !placed {
			if unreadable {
				return nil
			}
			return capacityPreCheckError(
				"refusing to roll %s: the new job needs %d MiB and %dm CPU and no host has that much unreserved after old job %s on host %s stops",
				typ, needMem>>20, needCPU, j.ID, j.HostID)
		}
	}
	return nil
}
