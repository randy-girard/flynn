package main

import (
	"errors"
	"sync/atomic"
	"time"

	. "github.com/flynn/go-check"
	"github.com/inconshreveable/log15"
	. "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/random"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

// After a host refuses AddJob, the retry goes to another eligible host.
// Every seed and a fresh map per seed cover the shuffle and the map order:
// a random pick put eleven retries in a row on the refusing host in CI
// (TestFailingAddJob, EDGE-41).
func (TestSuite) TestPlacementRetryAvoidsHostWhoseAddJobFailed(c *C) {
	defer reseedMathRandomly()
	for seed := int64(1); seed <= 200; seed++ {
		random.Math.Seed(seed)
		s := &Scheduler{
			isLeader:      typeconv.BoolPtr(true),
			jobs:          make(Jobs),
			volumes:       make(map[string]*Volume),
			hostVolumeIDs: make(map[string]map[string]struct{}),
			hosts:         make(map[string]*Host),
			logger:        log15.New(),
		}
		s.logger.SetHandler(log15.DiscardHandler())
		s.hosts["failing_host"] = &Host{ID: "failing_host", Healthy: true}
		s.hosts["ok_host"] = &Host{ID: "ok_host", Healthy: true}
		job := &Job{ID: "job-1", Type: "slugbuilder", State: JobStatePending, AppID: "app", ReleaseID: "rel"}
		s.jobs[job.ID] = job

		s.noteAddJobFailure(job, "failing_host")
		req := &PlacementRequest{Job: job, Err: make(chan error, 1)}
		s.HandlePlacementRequest(req)
		if err := <-req.Err; err != nil {
			c.Fatalf("seed %d: %v", seed, err)
		}
		if req.Host == nil || req.Host.ID != "ok_host" {
			c.Fatalf("seed %d: retry placed on %v, want ok_host (failing_host just refused AddJob)", seed, req.Host)
		}
	}
}

// The refusing host is still used when it is the only eligible one.
func (TestSuite) TestPlacementRetryFallsBackToOnlyHost(c *C) {
	s := &Scheduler{
		isLeader:      typeconv.BoolPtr(true),
		jobs:          make(Jobs),
		volumes:       make(map[string]*Volume),
		hostVolumeIDs: make(map[string]map[string]struct{}),
		hosts:         map[string]*Host{"failing_host": {ID: "failing_host", Healthy: true}},
		logger:        log15.New(),
	}
	s.logger.SetHandler(log15.DiscardHandler())
	job := &Job{ID: "job-1", Type: "slugbuilder", State: JobStatePending, AppID: "app", ReleaseID: "rel"}
	s.jobs[job.ID] = job
	s.noteAddJobFailure(job, "failing_host")
	req := &PlacementRequest{Job: job, Err: make(chan error, 1)}
	s.HandlePlacementRequest(req)
	c.Assert(<-req.Err, IsNil)
	c.Assert(req.Host, NotNil)
	c.Assert(req.Host.ID, Equals, "failing_host")
}

type countingFailHost struct {
	*FakeHostClient
	calls int32
}

func (h *countingFailHost) AddJob(*host.Job) error {
	atomic.AddInt32(&h.calls, 1)
	return errors.New("fail")
}

// StartJob records the refusing host, so with two hosts up a job never
// reaches the refusing host twice before it starts on the other one.
func (TestSuite) TestStartJobRetryAfterAddJobFailureUsesOtherHost(c *C) {
	for run := 0; run < 12; run++ {
		failing := &countingFailHost{FakeHostClient: NewFakeHostClient("failing_host", false)}
		okHost := NewFakeHostClient("ok_host", false)
		events := make(chan *host.Event, 16)
		stream, err := okHost.StreamEvents("", events)
		c.Assert(err, IsNil)
		cluster := NewFakeCluster()
		cluster.AddHost(failing)
		cluster.AddHost(okHost)

		s := newTestScheduler(c, cluster, true, map[string]int{"web": 0})
		s.logger.SetHandler(log15.DiscardHandler())
		go s.Run()
		s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{"web": 1}})
	wait:
		for {
			select {
			case e := <-events:
				if e.Event == host.JobEventCreate || e.Event == host.JobEventStart {
					break wait
				}
			case <-time.After(10 * time.Second):
				c.Fatalf("run %d: job never started on ok_host (failing_host AddJob calls: %d)", run, atomic.LoadInt32(&failing.calls))
			}
		}
		calls := atomic.LoadInt32(&failing.calls)
		s.Stop()
		stream.Close()
		if calls > 1 {
			c.Fatalf("run %d: failing_host got %d AddJob calls before ok_host ran the job, want at most 1", run, calls)
		}
	}
}
