package updaterdeploy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
)

// EDGE-116: upgrading a v20261114.0 cluster runs head's updater against the
// v20261114.0 scheduler, which still has the NEW-04 bug. oldScheduler copies
// the parts of that scheduler's formation handling the rollout meets:
//
//   - handleFormation ignores an event older than what it holds, and applies
//     one only when the processes differ from the original counts or the
//     tags differ (v20261114.0 FormationTagsEqual: only keys of the incoming
//     tags are compared).
//   - a leader promotion starts a formation sync: the active formations are
//     read at once, the rest of the fetch takes a while (25 s on the dev
//     cluster while discoverd was being replaced), and rectify is held
//     until it applies.
//   - applying that sync zeroes every formation missing from the snapshot
//     (f.Processes = nil, original counts untouched) and stops its jobs as
//     scale-down. Later events with the same counts and tags change nothing.
//   - rectify starts one job on each host a formation's tags allow that has
//     no running job of the app.
type oldSchedFormation struct {
	processes map[string]int
	original  map[string]int
	tags      map[string]map[string]string
	updated   int
}

type oldSchedulerController struct {
	*fakeInfraController
	clock   int
	updated map[string]int
	sched   map[string]*oldSchedFormation
	hosts   []*oldSchedHost

	// snapshot is the active-formation list of a sync in flight.
	snapshot  map[string]oldSchedFormation
	inFlight  bool
	fetches   int
	zeroed    []string
	afterPut  func(f *ct.Formation)
	scheduled int
}

func newOldSchedulerController(t *testing.T) *oldSchedulerController {
	c := &oldSchedulerController{
		fakeInfraController: newFakeInfraController(t, "layer-old"),
		updated:             map[string]int{},
		sched:               map[string]*oldSchedFormation{},
	}
	c.updated["rel-old"] = 0
	c.sched["rel-old"] = &oldSchedFormation{processes: map[string]int{"app": 1}, original: map[string]int{"app": 1}}
	return c
}

func (c *oldSchedulerController) PutFormation(f *ct.Formation) error {
	if err := c.fakeInfraController.PutFormation(f); err != nil {
		return err
	}
	c.clock++
	c.updated[f.ReleaseID] = c.clock
	c.handleFormation(f.ReleaseID, oldSchedFormation{processes: copyProcesses(f.Processes), tags: copyTags(f.Tags), updated: c.clock})
	if c.afterPut != nil {
		c.afterPut(f)
	}
	return nil
}

func (c *oldSchedulerController) handleFormation(rel string, ef oldSchedFormation) {
	cur := c.sched[rel]
	if cur == nil {
		c.sched[rel] = &oldSchedFormation{processes: copyProcesses(ef.processes), original: copyProcesses(ef.processes), tags: copyTags(ef.tags), updated: ef.updated}
		c.rectify(rel)
		return
	}
	if cur.updated > ef.updated {
		return // "ignoring stale formation change"
	}
	cur.updated = ef.updated
	if processesEqual(ef.processes, cur.original) && oldFormationTagsEqual(cur.tags, ef.tags) {
		return
	}
	cur.tags = copyTags(ef.tags)
	cur.processes = copyProcesses(ef.processes)
	cur.original = copyProcesses(ef.processes)
	c.rectify(rel)
}

// beginFetch is a leader promotion: the active formations are read now.
func (c *oldSchedulerController) beginFetch() {
	c.fetches++
	c.inFlight = true
	c.snapshot = map[string]oldSchedFormation{}
	for rel, f := range c.formations {
		if processCount(f.Processes) > 0 {
			c.snapshot[rel] = oldSchedFormation{processes: copyProcesses(f.Processes), tags: copyTags(f.Tags), updated: c.updated[rel]}
		}
	}
}

// applyFetch is v20261114.0 applyFormationFetch.
func (c *oldSchedulerController) applyFetch() {
	if !c.inFlight {
		return
	}
	c.inFlight = false
	for rel, ef := range c.snapshot {
		c.handleFormation(rel, ef)
	}
	for rel, f := range c.sched {
		if _, ok := c.snapshot[rel]; ok || processCount(f.processes) == 0 {
			continue
		}
		// "formation should not be active, scaling down"
		f.processes = nil
		c.zeroed = append(c.zeroed, rel)
		for _, h := range c.hosts {
			for id, j := range h.jobs {
				if j.Job != nil && j.Job.Metadata[hostInfraReleaseKey] == rel && (j.Status == host.StatusRunning || j.Status == host.StatusStarting) {
					j.Status = host.StatusDone
					h.jobs[id] = j
				}
			}
		}
	}
	for rel := range c.sched {
		c.rectify(rel)
	}
}

func (c *oldSchedulerController) rectify(rel string) {
	f := c.sched[rel]
	if c.inFlight || f == nil || f.processes["app"] <= 0 {
		return
	}
	var release *ct.Release
	for _, r := range c.releases {
		if r.ID == rel {
			release = r
		}
	}
	if release == nil {
		return
	}
	for _, h := range c.hosts {
		if !ct.PlacementTagsMatchHost(f.tags["app"], h.id, nil) || findRunningHostInfraJob(h.jobs, "flannel") != nil {
			continue
		}
		c.scheduled++
		id := fmt.Sprintf("%s-sched-%d", h.id, c.scheduled)
		h.jobs[id] = host.ActiveJob{Status: host.StatusRunning, Job: &host.Job{
			ID:         id,
			Metadata:   map[string]string{"flynn-controller.app_name": "flannel", hostInfraReleaseKey: rel},
			Mountspecs: []*host.Mountspec{{ID: jobLayerIDsForArtifact(c.artifacts[release.ArtifactIDs[0]])}},
		}}
	}
}

// oldSchedHost is a host whose jobs the old scheduler watches: a job of a
// formation it holds at zero is stopped as soon as it starts.
type oldSchedHost struct {
	fakeInfraHost
	c *oldSchedulerController
	// onStop runs after a StopJob (the discoverd swap moves the scheduler
	// leader).
	onStop func(h *oldSchedHost, id string)
}

func (h *oldSchedHost) StopJob(id string) error {
	err := h.fakeInfraHost.StopJob(id)
	if h.onStop != nil {
		h.onStop(h, id)
	}
	return err
}

func (h *oldSchedHost) AddJob(job *host.Job) error {
	if err := h.fakeInfraHost.AddJob(job); err != nil {
		return err
	}
	if f := h.c.sched[job.Metadata[hostInfraReleaseKey]]; f != nil && !h.c.inFlight && processCount(f.processes) == 0 {
		j := h.jobs[job.ID]
		j.Status = host.StatusDone
		h.jobs[job.ID] = j
	}
	return nil
}

func oldFormationTagsEqual(a, b map[string]map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for typ, tags := range b {
		for k, v := range tags {
			if w, ok := a[typ][k]; !ok || w != v {
				return false
			}
		}
	}
	return true
}

func processesEqual(a, b map[string]int) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

func copyTags(in map[string]map[string]string) map[string]map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for typ, tags := range in {
		inner := make(map[string]string, len(tags))
		for k, v := range tags {
			inner[k] = v
		}
		out[typ] = inner
	}
	return out
}

func oldSchedulerCluster(t *testing.T) (*oldSchedulerController, []*oldSchedHost) {
	c := newOldSchedulerController(t)
	var hosts []*oldSchedHost
	for i, id := range []string{"devnode1", "devnode2", "devnode3"} {
		jobID := fmt.Sprintf("old-%d", i+1)
		hosts = append(hosts, &oldSchedHost{
			fakeInfraHost: fakeInfraHost{id: id, jobs: map[string]host.ActiveJob{jobID: infraJob(jobID, "rel-old", "layer-old")}},
			c:             c,
		})
	}
	c.hosts = hosts
	return c, hosts
}

func runOldSchedulerRollout(t *testing.T, c *oldSchedulerController, hosts []*oldSchedHost) error {
	t.Helper()
	grace, delay, backoff, timeout := hostInfraSchedulerGrace, hostInfraFormationRetryDelay, hostInfraRestartBackoff, hostInfraReplaceTimeout
	hostInfraSchedulerGrace, hostInfraFormationRetryDelay, hostInfraRestartBackoff, hostInfraReplaceTimeout = 0, 0, time.Millisecond, 5*time.Second
	t.Cleanup(func() {
		hostInfraSchedulerGrace, hostInfraFormationRetryDelay, hostInfraRestartBackoff, hostInfraReplaceTimeout = grace, delay, backoff, timeout
	})
	img := newInfraImage(t)
	r, err := prepareHostInfraRollout(c, "flannel", img, nil)
	if err != nil {
		t.Fatal(err)
	}
	list := make([]hostInfraHost, 0, len(hosts))
	for _, h := range hosts {
		list = append(list, h)
	}
	return replaceHostInfraJobs(list, map[string]*ct.Artifact{"flannel": img}, map[string]*hostInfraRollout{"flannel": r}, false, nil)
}

func assertOldSchedulerRolloutKeptJobs(t *testing.T, c *oldSchedulerController, hosts []*oldSchedHost, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("rollout failed under the v20261114.0 scheduler (zeroed %v): %v", c.zeroed, err)
	}
	if len(c.created) != 1 {
		t.Fatalf("created %d releases, want 1", len(c.created))
	}
	newID := c.created[0].ID
	for _, h := range hosts {
		j := findRunningHostInfraJobWithLayers(h.jobs, "flannel", []string{"layer-new"}, newID)
		if j == nil {
			t.Fatalf("%s has no running %s job after the rollout (scheduler zeroed %v)", h.id, newID, c.zeroed)
		}
	}
	if f := c.sched[newID]; f == nil || f.processes["app"] != 1 {
		t.Fatalf("old scheduler holds %s at %+v, want app=1", newID, f)
	}
}

// The failed run: stopping the old job on devnode1 (discoverd there) moved
// the scheduler leader, whose formation sync read the active formations at
// once and applied them only after the updater had PUT the new release's
// formation for devnode1. The new formation was missing from that snapshot,
// so it was zeroed, and every job the updater then started was stopped right
// away ("3 started jobs were stopped right away").
func TestHostInfraRolloutSurvivesOldSchedulerSyncAcrossDiscoverdSwap(t *testing.T) {
	c, hosts := oldSchedulerCluster(t)
	hosts[0].onStop = func(h *oldSchedHost, id string) {
		if strings.HasPrefix(id, "old-") && c.fetches == 0 {
			c.beginFetch()
		}
	}
	c.afterPut = func(f *ct.Formation) {
		if c.inFlight && len(c.created) == 1 && f.ReleaseID == c.created[0].ID && strings.Contains(f.Tags["app"][ct.FormationHostIDsTag], "devnode1") {
			c.applyFetch()
		}
	}

	err := runOldSchedulerRollout(t, c, hosts)

	if c.fetches != 1 {
		t.Fatalf("fetches=%d, want the promotion sync to run", c.fetches)
	}
	assertOldSchedulerRolloutKeptJobs(t, c, hosts, err)
	if len(c.zeroed) != 0 {
		t.Fatalf("the old scheduler zeroed %v; the new formation must be in every sync snapshot taken during the swap", c.zeroed)
	}
}

// Defense in depth: a sync whose snapshot predates even the rollout's first
// write zeroes the new formation anyway. The old scheduler then ignores a
// PUT with the same counts and tags, so restarting the job alone is stopped
// every time. A PUT with changed tags makes it apply the formation again.
func TestHostInfraRolloutRestoresFormationZeroedByOldScheduler(t *testing.T) {
	c, hosts := oldSchedulerCluster(t)
	c.beginFetch()
	c.afterPut = func(f *ct.Formation) {
		if c.inFlight && len(c.created) == 1 && f.ReleaseID == c.created[0].ID && strings.Contains(f.Tags["app"][ct.FormationHostIDsTag], "devnode1") {
			c.applyFetch()
		}
	}

	err := runOldSchedulerRollout(t, c, hosts)

	if len(c.zeroed) == 0 {
		t.Fatal("the fixture did not zero the new formation; the test proves nothing")
	}
	assertOldSchedulerRolloutKeptJobs(t, c, hosts, err)
}
