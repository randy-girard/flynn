package main

import (
	"fmt"
	"sync/atomic"
	"time"

	. "github.com/flynn/go-check"
	"github.com/inconshreveable/log15"
	. "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

// spreadFormation is a non-omni formation with two web replicas, like the
// controller app.
func spreadFormation() *Formation {
	return &Formation{
		OriginalProcesses: Processes{"web": 2},
		ExpandedFormation: &ct.ExpandedFormation{
			App:       &ct.App{ID: "ctl", Name: "ctl"},
			Release:   &ct.Release{ID: "rel", Processes: map[string]ct.ProcessType{"web": {}}},
			Processes: map[string]int{"web": 2},
		},
	}
}

func spreadJob(f *Formation, id, hostID string, state JobState) *Job {
	return &Job{ID: id, AppID: f.App.ID, ReleaseID: f.Release.ID, Type: "web", Formation: f, HostID: hostID, State: state}
}

func spreadScheduler(hosts ...string) *Scheduler {
	s := &Scheduler{jobs: Jobs{}, hosts: map[string]*Host{}, logger: log15.New()}
	s.logger.SetHandler(log15.DiscardHandler())
	for _, id := range hosts {
		s.hosts[id] = &Host{ID: id, Healthy: true}
	}
	return s
}

func spreadCounts(s *Scheduler, job *Job) map[string]int {
	return s.jobs.GetHostSpreadCounts(job.Formation.key(), job.Type)
}

// Run C of EDGE-47 (EDGE-112): devnode2 was lost, the replacement for its
// controller web job first went to devnode3, which refused AddJob with
// "ratelimited: maximum concurrent AddJob calls running". The EDGE-41 retry
// rule then skipped devnode3, and devnode1, which already ran the other web
// job, was the only host left. One transient refusal must not stack the two
// replicas while the refusing host is still the only one without a replica.
func (TestSuite) TestSpreadReplicaRetryKeepsAntiAffinityAfterRefusal(c *C) {
	defer reseedMathRandomly()
	for seed := int64(1); seed <= 200; seed++ {
		random.Math.Seed(seed)
		s := spreadScheduler("devnode1", "devnode3")
		f := spreadFormation()
		s.jobs["peer"] = spreadJob(f, "peer", "devnode1", JobStateRunning)
		job := spreadJob(f, "new", "", JobStatePending)
		s.jobs[job.ID] = job

		s.noteAddJobFailure(job, "devnode3")
		got := s.pickHost(job, spreadCounts(s, job))
		if got == nil || got.ID != "devnode3" {
			c.Fatalf("seed %d: retry placed on %v, want devnode3 (devnode1 already runs a web replica)", seed, got)
		}
	}
}

// A host that keeps refusing must not hold a replica back for ever: after a
// few refusals in a row the retry takes the other host, even if that stacks.
func (TestSuite) TestSpreadReplicaRetryStacksAfterRepeatedRefusals(c *C) {
	s := spreadScheduler("devnode1", "devnode3")
	f := spreadFormation()
	s.jobs["peer"] = spreadJob(f, "peer", "devnode1", JobStateRunning)
	job := spreadJob(f, "new", "", JobStatePending)
	s.jobs[job.ID] = job
	for i := 0; i < 10; i++ {
		s.noteAddJobFailure(job, "devnode3")
	}
	got := s.pickHost(job, spreadCounts(s, job))
	c.Assert(got, NotNil)
	c.Assert(got.ID, Equals, "devnode1")
}

// Stopped and stopping peers are gone or going: a host that only has such
// a peer counts as having none.
func (TestSuite) TestSpreadReplicaIgnoresStoppedPeers(c *C) {
	defer reseedMathRandomly()
	for seed := int64(1); seed <= 50; seed++ {
		random.Math.Seed(seed)
		s := spreadScheduler("h1", "h2")
		f := spreadFormation()
		s.jobs["stopped"] = spreadJob(f, "stopped", "h1", JobStateStopped)
		s.jobs["stopping"] = spreadJob(f, "stopping", "h1", JobStateStopping)
		s.jobs["peer"] = spreadJob(f, "peer", "h2", JobStateRunning)
		job := spreadJob(f, "new", "", JobStatePending)
		s.jobs[job.ID] = job
		got := s.pickHost(job, spreadCounts(s, job))
		if got == nil || got.ID != "h1" {
			c.Fatalf("seed %d: placed on %v, want h1 (its only web job is stopped or stopping)", seed, got)
		}
	}
}

// Replica count of the app and type decides first; among hosts with the
// same count, the host running fewer active jobs in total wins.
func (TestSuite) TestSpreadReplicaTieBreaksOnHostLoad(c *C) {
	defer reseedMathRandomly()
	for seed := int64(1); seed <= 50; seed++ {
		random.Math.Seed(seed)
		s := spreadScheduler("busy", "idle", "peer")
		f := spreadFormation()
		other := spreadFormation()
		other.App = &ct.App{ID: "other"}
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("other-%d", i)
			s.jobs[id] = spreadJob(other, id, "busy", JobStateRunning)
		}
		s.jobs["stopped-on-idle"] = spreadJob(other, "stopped-on-idle", "idle", JobStateStopped)
		s.jobs["stopped-on-idle-2"] = spreadJob(other, "stopped-on-idle-2", "idle", JobStateStopped)
		s.jobs["peer"] = spreadJob(f, "peer", "peer", JobStateRunning)
		job := spreadJob(f, "new", "", JobStatePending)
		s.jobs[job.ID] = job
		got := s.pickHost(job, spreadCounts(s, job))
		if got == nil || got.ID != "idle" {
			c.Fatalf("seed %d: placed on %v, want idle (no replica, fewest active jobs)", seed, got)
		}
	}
}

// Ties of count and load are still broken at random.
func (TestSuite) TestSpreadReplicaRandomAmongEquals(c *C) {
	defer reseedMathRandomly()
	seen := map[string]int{}
	for seed := int64(1); seed <= 100; seed++ {
		random.Math.Seed(seed)
		s := spreadScheduler("h1", "h2", "h3")
		f := spreadFormation()
		s.jobs["peer"] = spreadJob(f, "peer", "h3", JobStateRunning)
		job := spreadJob(f, "new", "", JobStatePending)
		got := s.pickHost(job, spreadCounts(s, job))
		c.Assert(got, NotNil)
		seen[got.ID]++
	}
	c.Assert(seen["h3"], Equals, 0)
	if seen["h1"] == 0 || seen["h2"] == 0 {
		c.Fatalf("picks %v: equal hosts must share placements", seen)
	}
}

// A host with no replica that is unhealthy or shutting down is not used.
func (TestSuite) TestSpreadReplicaSkipsUnhealthyHost(c *C) {
	s := spreadScheduler("h1", "down", "shutdown")
	s.hosts["down"].Healthy = false
	s.hosts["shutdown"].Shutdown = true
	f := spreadFormation()
	s.jobs["peer"] = spreadJob(f, "peer", "h1", JobStateRunning)
	job := spreadJob(f, "new", "", JobStatePending)
	for i := 0; i < 20; i++ {
		got := s.pickHost(job, spreadCounts(s, job))
		c.Assert(got, NotNil)
		c.Assert(got.ID, Equals, "h1")
	}
}

// refuseOnceHost refuses its first AddJob as flynn-host does when too many
// AddJob calls run at once, then accepts.
type refuseOnceHost struct {
	*FakeHostClient
	calls int32
}

func (h *refuseOnceHost) AddJob(job *host.Job) error {
	if atomic.AddInt32(&h.calls, 1) == 1 {
		return httphelper.JSONError{Code: httphelper.RatelimitedErrorCode, Message: "maximum concurrent AddJob calls running, try again later"}
	}
	return h.FakeHostClient.AddJob(job)
}

// The same through StartJob: scaling web from 1 to 2 with the second host
// refusing the first AddJob must still put the replicas on different hosts.
func (TestSuite) TestStartJobSpreadsReplicaAfterRateLimitedAddJob(c *C) {
	for run := 0; run < 5; run++ {
		h1 := NewFakeHostClient("h1", false)
		h2 := &refuseOnceHost{FakeHostClient: NewFakeHostClient("h2", false)}
		cl := NewFakeCluster()
		cl.AddHost(h1)
		s := newTestScheduler(c, cl, true, map[string]int{"web": 1})
		s.logger.SetHandler(log15.DiscardHandler())
		go s.Run()

		waitActive := func(want map[string]int) {
			deadline := time.Now().Add(10 * time.Second)
			for {
				got := map[string]int{}
				for _, j := range s.InternalState().Jobs {
					if j.Type == "web" && (j.State == JobStateStarting || j.State == JobStateRunning) {
						got[j.HostID]++
					}
				}
				if fmt.Sprint(got) == fmt.Sprint(want) {
					return
				}
				if time.Now().After(deadline) {
					c.Fatalf("run %d: web jobs per host %v, want %v", run, got, want)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		waitActive(map[string]int{"h1": 1})
		cl.AddHost(h2)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, ok := s.InternalState().Hosts["h2"]; ok {
				break
			}
			if time.Now().After(deadline) {
				c.Fatalf("run %d: h2 was never followed", run)
			}
			time.Sleep(20 * time.Millisecond)
		}
		s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{"web": 2}})
		waitActive(map[string]int{"h1": 1, "h2": 1})
		c.Assert(atomic.LoadInt32(&h2.calls) >= 2, Equals, true)
		s.Stop()
	}
}

// newServiceTestScheduler is newTestScheduler with the processes
// registering a discoverd service, as the controller's web type does.
func newServiceTestScheduler(c *C, cl *FakeCluster, processes map[string]int, service bool) *TestScheduler {
	app := &ct.App{ID: testAppID, Name: testAppID}
	artifact := &ct.Artifact{ID: testArtifactId}
	release := NewRelease(testReleaseID, artifact, processes)
	if service {
		for typ, proc := range release.Processes {
			proc.Service = testAppID + "-" + typ
			release.Processes[typ] = proc
		}
	}
	cc := NewFakeControllerClient()
	cc.CreateApp(app)
	cc.CreateArtifact(artifact)
	cc.CreateRelease(app.ID, release)
	cc.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: processes})
	d := newFakeDiscoverd(true)
	events := make(chan *log15.Record, eventBufferSize)
	logger := log15.New()
	logger.SetHandler(log15.DiscardHandler())
	return &TestScheduler{NewScheduler(cl, cc, d, logger), c, events, d}
}

// markWebRunning moves every starting web job to running, as the service
// and router backend events do on a real cluster.
func markWebRunning(s *TestScheduler) {
	s.onLoop(func() {
		for _, j := range s.jobs {
			if j.Type == "web" && j.State == JobStateStarting {
				s.handleJobStatus(j, host.StatusRunning)
			}
		}
	})
}

func webJobsByHost(s *TestScheduler) map[string][]*Job {
	out := map[string][]*Job{}
	for _, j := range s.InternalState().Jobs {
		if j.Type != "web" || j.State == JobStateStopped {
			continue
		}
		out[j.HostID] = append(out[j.HostID], j)
	}
	return out
}

func waitWebJobs(c *C, s *TestScheduler, what string, ok func(map[string][]*Job) bool) map[string][]*Job {
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := webJobsByHost(s)
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			desc := map[string][]string{}
			for h, js := range got {
				for _, j := range js {
					desc[h] = append(desc[h], string(j.State))
				}
			}
			c.Fatalf("timed out waiting for %s: web jobs %v", what, desc)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Replicas stacked while only one host was up (EDGE-112) are spread out once
// another healthy host is there: a new job starts on the empty host first,
// and only once it is running does one of the stacked jobs stop.
func (TestSuite) TestStackedReplicasRebalancedWhenHostReturns(c *C) {
	h1 := NewFakeHostClient("h1", false)
	h2 := NewFakeHostClient("h2", false)
	cl := NewFakeCluster()
	cl.AddHost(h1)
	s := newServiceTestScheduler(c, cl, map[string]int{"web": 2}, true)
	var jobID uint64
	s.generateJobUUID = func() string { return fmt.Sprintf("job%d", atomic.AddUint64(&jobID, 1)) }
	go s.Run()
	defer s.Stop()

	waitWebJobs(c, s, "two web jobs on h1", func(m map[string][]*Job) bool { return len(m["h1"]) == 2 })
	markWebRunning(s)
	waitWebJobs(c, s, "both running", func(m map[string][]*Job) bool {
		return len(m["h1"]) == 2 && m["h1"][0].State == JobStateRunning && m["h1"][1].State == JobStateRunning
	})

	cl.AddHost(h2)
	got := waitWebJobs(c, s, "a new web job on h2", func(m map[string][]*Job) bool { return len(m["h2"]) == 1 })
	c.Assert(got["h2"][0].State, Equals, JobStateStarting)
	// The stacked jobs keep serving until the new one is up.
	time.Sleep(200 * time.Millisecond)
	got = webJobsByHost(s)
	c.Assert(len(got["h1"]), Equals, 2)
	for _, j := range got["h1"] {
		c.Assert(j.State, Equals, JobStateRunning)
	}

	markWebRunning(s)
	waitWebJobs(c, s, "one web job per host", func(m map[string][]*Job) bool {
		return len(m["h1"]) == 1 && len(m["h2"]) == 1 && m["h1"][0].State == JobStateRunning && m["h2"][0].State == JobStateRunning
	})
	// Nothing else moves, and the formation is not refilled on h1.
	time.Sleep(300 * time.Millisecond)
	got = webJobsByHost(s)
	c.Assert(len(got["h1"]), Equals, 1)
	c.Assert(len(got["h2"]), Equals, 1)
	c.Assert(len(got), Equals, 2)
}

// A new job that dies before it is up cancels the move: both stacked jobs
// keep running and no extra job is started for the formation.
func (TestSuite) TestRebalanceCancelledWhenNewJobDies(c *C) {
	h1 := NewFakeHostClient("h1", false)
	h2 := NewFakeHostClient("h2", false)
	cl := NewFakeCluster()
	cl.AddHost(h1)
	s := newServiceTestScheduler(c, cl, map[string]int{"web": 2}, true)
	var jobID uint64
	s.generateJobUUID = func() string { return fmt.Sprintf("job%d", atomic.AddUint64(&jobID, 1)) }
	go s.Run()
	defer s.Stop()

	waitWebJobs(c, s, "two web jobs on h1", func(m map[string][]*Job) bool { return len(m["h1"]) == 2 })
	markWebRunning(s)
	waitWebJobs(c, s, "both running", func(m map[string][]*Job) bool {
		return len(m["h1"]) == 2 && m["h1"][0].State == JobStateRunning && m["h1"][1].State == JobStateRunning
	})
	cl.AddHost(h2)
	got := waitWebJobs(c, s, "a new web job on h2", func(m map[string][]*Job) bool { return len(m["h2"]) == 1 })
	c.Assert(h2.CrashJob(got["h2"][0].ID), IsNil)
	waitWebJobs(c, s, "the new job gone", func(m map[string][]*Job) bool { return len(m["h2"]) == 0 })
	time.Sleep(300 * time.Millisecond)
	got = webJobsByHost(s)
	c.Assert(len(got["h1"]), Equals, 2)
	c.Assert(len(got["h2"]), Equals, 0)
	for _, j := range got["h1"] {
		c.Assert(j.State, Equals, JobStateRunning)
	}
}

// With a single host stacking is allowed and nothing is moved.
func (TestSuite) TestRebalanceLeavesStackOnSingleHost(c *C) {
	h1 := NewFakeHostClient("h1", false)
	cl := NewFakeCluster()
	cl.AddHost(h1)
	s := newServiceTestScheduler(c, cl, map[string]int{"web": 2}, true)
	go s.Run()
	defer s.Stop()

	waitWebJobs(c, s, "two web jobs on h1", func(m map[string][]*Job) bool { return len(m["h1"]) == 2 })
	markWebRunning(s)
	s.triggerSyncHosts()
	time.Sleep(300 * time.Millisecond)
	got := webJobsByHost(s)
	c.Assert(len(got), Equals, 1)
	c.Assert(len(got["h1"]), Equals, 2)
}

// A type without a discoverd service (a worker) is not moved: its "running"
// state says nothing about being ready, and a move can cut its work short.
func (TestSuite) TestRebalanceSkipsTypeWithoutService(c *C) {
	h1 := NewFakeHostClient("h1", false)
	h2 := NewFakeHostClient("h2", false)
	cl := NewFakeCluster()
	cl.AddHost(h1)
	s := newServiceTestScheduler(c, cl, map[string]int{"web": 2}, false)
	go s.Run()
	defer s.Stop()

	waitWebJobs(c, s, "two web jobs on h1", func(m map[string][]*Job) bool { return len(m["h1"]) == 2 })
	markWebRunning(s)
	cl.AddHost(h2)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := s.InternalState().Hosts["h2"]; ok {
			break
		}
		if time.Now().After(deadline) {
			c.Fatal("h2 was never followed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	got := webJobsByHost(s)
	c.Assert(len(got["h1"]), Equals, 2)
	c.Assert(len(got["h2"]), Equals, 0)
}
