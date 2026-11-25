package deployment

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	worker "github.com/randy-girard/flynn/controller/worker/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
)

// What "the new omni job on a host is serving" means (EDGE-42). The roll
// used to count a job that was only pending or starting, and to finish once
// any new job existed. A router deploy then stopped the next host's router
// before the previous host's new one listened, and a deploy right after
// another could finish with a host running no router at all.
//
// A host's new job now serves when:
//   - the controller reports it up (the host started the process);
//   - it has stayed up for omniStabilizeWindow, so a process that exits
//     right after it starts does not count;
//   - when the process type declares a discoverd service, an instance of
//     that service carries the job's FLYNN_JOB_ID (the router registers
//     router-api after its listeners are bound; the controller scheduler
//     registers controller-scheduler). A service whose instances carry no
//     job IDs at all cannot be matched and is not checked.

// omniStabilizeEnv sets omniStabilizeWindow on the controller worker: a Go
// duration ("5s") or whole seconds ("5"); "0" turns the window off.
const omniStabilizeEnv = "FLYNN_OMNI_STABILIZE"

// omniStabilizeDefault is the window when the env is unset. It is per host,
// so a three-host roll takes about 15 s longer.
const omniStabilizeDefault = 5 * time.Second

var omniStabilizeWindow = parseOmniStabilize(os.Getenv(omniStabilizeEnv))

// omniCoverWaitLimit bounds the final wait for every host to run an up job
// of the new release. It is longer than a step's wait because the scheduler
// places the jobs on the remaining hosts only once the pins are cleared.
var omniCoverWaitLimit = 60 * time.Second

// omniServiceInstances lists a discoverd service's instances. A var so
// tests can stand in for discoverd.
var omniServiceInstances = func(service string) ([]*discoverd.Instance, error) {
	return discoverd.InstancesOrEmpty(discoverd.NewService(service))
}

func parseOmniStabilize(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return omniStabilizeDefault
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n < 0 {
			return omniStabilizeDefault
		}
		return time.Duration(n) * time.Second
	}
	if d, err := time.ParseDuration(raw); err == nil && d >= 0 {
		return d
	}
	return omniStabilizeDefault
}

// omniTargetHosts is the cluster's current host list, without hosts that
// are shutting down, narrowed to the hosts the scheduler may run this omni
// type on, by the new formation's tags (and
// the runtime profile's). Pins (flynn-host-ids) are left out: this is the
// set the roll must cover once the pins are cleared.
func (d *DeployJob) omniTargetHosts(typ string) ([]string, error) {
	hosts, err := d.client.HostList()
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	if d.newFormation != nil {
		for k, v := range d.newFormation.Tags[typ] {
			if k != ct.FormationHostIDsTag {
				tags[k] = v
			}
		}
	}
	if d.newRelease != nil {
		if name := strings.ToLower(strings.TrimSpace(d.newRelease.Processes[typ].RuntimeProfile)); name != "" {
			profiles, err := d.client.ListRuntimeProfiles()
			if err != nil {
				return nil, err
			}
			for _, p := range profiles {
				if p != nil && strings.ToLower(strings.TrimSpace(p.Name)) == name {
					tags = ct.MergePlacementTags(tags, p.Tags)
				}
			}
		}
	}
	ids := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if h == nil || h.ID == "" || h.Shutdown {
			continue
		}
		if ct.PlacementTagsMatchHost(tags, h.ID, h.Tags) {
			ids = append(ids, h.ID)
		}
	}
	return uniqueSortedHostIDs(ids), nil
}

// noteOmniKnownHosts records the hosts that run (or ran during this roll) a
// job of either release. An overlay omni type (the controller scheduler)
// only has to cover these: a host that is still joining is left to the
// scheduler, as before (an add-node waited 30 s for a host that had not
// started its first jobs yet).
func (d *DeployJob) noteOmniKnownHosts(ids ...string) {
	if d.omniKnownHosts == nil {
		d.omniKnownHosts = map[string]struct{}{}
	}
	for _, id := range ids {
		if id != "" {
			d.omniKnownHosts[id] = struct{}{}
		}
	}
}

// omniCoverHosts narrows the target hosts to the ones the roll must see
// serving at the end: every target host for a host-network type (the
// router: a host without one has no ingress), the known hosts for an
// overlay type.
func (d *DeployJob) omniCoverHosts(typ string, targets []string) []string {
	if d.processHostNetwork(typ) {
		return targets
	}
	out := make([]string, 0, len(targets))
	for _, id := range targets {
		if _, ok := d.omniKnownHosts[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// omniHostState is why a host does not serve the new release yet; empty
// when it does.
func (d *DeployJob) omniHostState(typ, host string, jobs []*ct.Job, instances map[string]bool, now time.Time) string {
	perHost := d.Processes[typ]
	if perHost < 1 {
		perHost = 1
	}
	var states []string
	serving := 0
	var waiting time.Duration
	for _, job := range jobs {
		if job == nil || job.ReleaseID != d.NewReleaseID || job.Type != typ || job.HostID != host {
			continue
		}
		if job.State != ct.JobStateUp {
			if jobHoldsHostPort(job) {
				states = append(states, string(job.State))
			}
			continue
		}
		since, ok := d.omniUpSince[job.ID]
		if !ok {
			since = now
			d.omniUpSince[job.ID] = now
		}
		if up := now.Sub(since); up < omniStabilizeWindow {
			states = append(states, fmt.Sprintf("up %s of %s", up.Round(100*time.Millisecond), omniStabilizeWindow))
			if rest := omniStabilizeWindow - up; waiting == 0 || rest < waiting {
				waiting = rest
			}
			continue
		}
		if instances != nil && !instances[job.ID] {
			states = append(states, "up, not registered in "+d.newRelease.Processes[typ].Service)
			continue
		}
		serving++
	}
	if serving >= perHost {
		return ""
	}
	if len(states) == 0 {
		return "no job"
	}
	return strings.Join(states, ", ")
}

// forgetOmniUpSince drops the up times of jobs that are no longer up, so a
// job that went down and came back starts its window again.
func (d *DeployJob) forgetOmniUpSince(jobs []*ct.Job) {
	up := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		if job != nil && job.State == ct.JobStateUp {
			up[job.ID] = struct{}{}
		}
	}
	for id := range d.omniUpSince {
		if _, ok := up[id]; !ok {
			delete(d.omniUpSince, id)
		}
	}
}

// omniRegisteredJobs is the set of job IDs registered in the process type's
// discoverd service, or nil when the type declares none or its instances
// carry no job IDs.
func (d *DeployJob) omniRegisteredJobs(typ string) (map[string]bool, error) {
	if d.newRelease == nil {
		return nil, nil
	}
	service := d.newRelease.Processes[typ].Service
	if service == "" {
		return nil, nil
	}
	insts, err := omniServiceInstances(service)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		if id := inst.Meta["FLYNN_JOB_ID"]; id != "" {
			ids[id] = true
		}
	}
	if len(ids) == 0 && len(insts) > 0 {
		return nil, nil
	}
	return ids, nil
}

// waitNewOmniJobsUp waits until every host in waitHosts (and, with cover,
// every host the roll must cover) runs a serving job of the new release.
// The host list is read on every poll: a host that left the cluster is no
// longer waited for, and one that joined is covered. JobList, HostList and
// discoverd errors are missed polls until the deadline (EDGE-65).
func (d *DeployJob) waitNewOmniJobsUp(typ string, waitHosts []string, cover bool, log log15.Logger) error {
	if d.omniUpSince == nil {
		d.omniUpSince = map[string]time.Time{}
	}
	limit := omniStopWaitLimit
	if cover {
		limit = omniCoverWaitLimit
	}
	deadline := time.Now().Add(limit)
	polls := newMissedPolls("omni roll: waiting for new "+typ+" jobs to serve", "JobList")
	var targets []string
	haveTargets := false
	var missing []string
	describe := func() string {
		if missing == nil {
			return fmt.Sprintf("want %v, no answer yet", waitHosts)
		}
		return strings.Join(missing, "; ")
	}
	for {
		jobs, err := d.client.JobList(d.AppID)
		var instances map[string]bool
		if err == nil {
			var hosts []string
			if hosts, err = d.omniTargetHosts(typ); err == nil {
				targets, haveTargets = hosts, true
			} else if haveTargets && !cover {
				// a step only needs the list to drop hosts that left
				err = nil
			}
		}
		if err == nil {
			instances, err = d.omniRegisteredJobs(typ)
		}
		if err != nil {
			if err := polls.miss(err, deadline, log); err != nil {
				return err
			}
			if time.Now().After(deadline) {
				return polls.timedOut(fmt.Sprintf("timed out waiting for new %s jobs to start (%s)", typ, describe()))
			}
			select {
			case <-d.stop:
				return worker.ErrStopped
			case <-time.After(polls.wait(omniStopPoll)):
			}
			continue
		}
		polls.answered(log)
		d.forgetOmniUpSince(jobs)
		d.noteOmniKnownHosts(omniHostIDsIn(jobs, typ, d.NewReleaseID)...)

		listed := make(map[string]struct{}, len(targets))
		for _, id := range targets {
			listed[id] = struct{}{}
		}
		var required []string
		for _, id := range waitHosts {
			if _, ok := listed[id]; ok || !haveTargets {
				required = append(required, id)
			} else {
				log.Info("not waiting for a host that left the cluster", "host.id", id)
			}
		}
		if cover {
			required = append(required, d.omniCoverHosts(typ, targets)...)
		}
		required = uniqueSortedHostIDs(required)

		now := time.Now()
		missing = missing[:0]
		for _, host := range required {
			if state := d.omniHostState(typ, host, jobs, instances, now); state != "" {
				missing = append(missing, host+": "+state)
			}
		}
		if len(missing) == 0 {
			if cover && len(required) == 0 {
				return fmt.Errorf("omni roll: no host can run %s jobs of release %s", typ, d.NewReleaseID)
			}
			log.Info("new omni jobs serve", "hosts", required, "window", omniStabilizeWindow)
			return nil
		}
		sort.Strings(missing)
		log.Info("waiting for new omni jobs to serve", "want", required, "missing", missing)
		if time.Now().After(deadline) {
			return polls.timedOut(fmt.Sprintf("timed out waiting for new %s jobs to start (%s)", typ, describe()))
		}
		select {
		case <-d.stop:
			return worker.ErrStopped
		case <-time.After(polls.wait(omniStopPoll)):
		}
	}
}
