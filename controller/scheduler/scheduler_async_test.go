package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/flynn/go-check"
	. "github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

// blockUntilRelease runs hookBody until release is called. entered is closed
// on the first call so a test can wait until the scheduler is inside the RPC.
func blockUntilRelease() (entered <-chan struct{}, release func(), hook func()) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	var onceStart sync.Once
	var onceStop sync.Once
	return started, func() { onceStop.Do(func() { close(unblock) }) }, func() {
		onceStart.Do(func() { close(started) })
		<-unblock
	}
}

func waitSignal(c *C, ch <-chan struct{}, what string) {
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		c.Fatalf("timed out waiting for %s", what)
	}
}

func startIdleScheduler(c *C, processes map[string]int) (*TestScheduler, *FakeCluster, *FakeHostClient, *FakeHostClient) {
	a := NewFakeHostClient("hosta", false)
	b := NewFakeHostClient("hostb", false)
	cluster := newTestCluster(map[string]utils.HostClient{a.ID(): a, b.ID(): b})
	s := newTestScheduler(c, cluster, true, processes)
	go s.Run()
	s.waitRectify()
	return s, cluster, a, b
}

func (TestSuite) TestBlockedListActiveJobsDoesNotStallOtherPlacement(c *C) {
	s, _, a, b := startIdleScheduler(c, map[string]int{testJobType: 0})
	entered, release, hook := blockUntilRelease()
	defer release()
	defer s.Stop()
	a.OnListActiveJobs = hook

	added := make(chan struct{}, 1)
	note := func(*host.Job) {
		select {
		case added <- struct{}{}:
		default:
		}
	}
	a.OnAddJob = note
	b.OnAddJob = note
	s.triggerSyncJobs()
	waitSignal(c, entered, "ListActiveJobs on host A")

	s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 1}})
	select {
	case <-added:
	case <-time.After(time.Second):
		c.Fatal("AddJob did not run while host A ListActiveJobs was blocked")
	}
}

func (TestSuite) TestBlockedVolumeListDoesNotStallOtherApp(c *C) {
	// Persistent volumes are the cache-miss ListVolumes path. Ephemeral
	// volumes are not adopted, so placement never lists the host.
	a := NewFakeHostClient("hosta", false)
	b := NewFakeHostClient("hostb", false)
	cluster := newTestCluster(map[string]utils.HostClient{a.ID(): a, b.ID(): b})
	s := newTestScheduler(c, cluster, true, map[string]int{testJobType: 0})
	cc := s.ControllerClient.(*FakeControllerClient)
	releaseObj, err := cc.GetRelease(testReleaseID)
	c.Assert(err, IsNil)
	web := releaseObj.Processes[testJobType]
	web.Volumes = []ct.VolumeReq{{Path: "/data"}}
	releaseObj.Processes[testJobType] = web
	s.volumes["vol-a"] = &Volume{Volume: ct.Volume{
		ID:        "vol-a",
		HostID:    a.ID(),
		AppID:     testAppID,
		ReleaseID: testReleaseID,
		JobType:   testJobType,
		State:     ct.VolumeStateCreated,
		VolumeReq: ct.VolumeReq{Path: "/data"},
	}}
	go s.Run()
	defer s.Stop()
	s.waitRectify()

	entered, release, hook := blockUntilRelease()
	defer release()
	a.OnListVolumes = hook
	s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 1}})
	waitSignal(c, entered, "ListVolumes on host A")

	app := &ct.App{ID: "app-2", Name: "app-2"}
	artifact := &ct.Artifact{ID: random.UUID()}
	rel := NewRelease(random.UUID(), artifact, map[string]int{"worker": 1})
	c.Assert(s.CreateApp(app), IsNil)
	c.Assert(s.CreateArtifact(artifact), IsNil)
	c.Assert(s.CreateRelease(app.ID, rel), IsNil)
	added := make(chan struct{}, 1)
	note := func(*host.Job) {
		select {
		case added <- struct{}{}:
		default:
		}
	}
	a.OnAddJob = note
	b.OnAddJob = note
	c.Assert(s.PutFormation(&ct.Formation{AppID: app.ID, ReleaseID: rel.ID, Processes: map[string]int{"worker": 1}}), IsNil)
	select {
	case <-added:
	case <-time.After(time.Second):
		c.Fatal("AddJob for another app did not run while host A ListVolumes was blocked")
	}
}

func (TestSuite) TestBlockedHostStatusDoesNotStallPlacement(c *C) {
	s, cluster, a, b := startIdleScheduler(c, map[string]int{testJobType: 0})
	entered, release, hook := blockUntilRelease()
	defer release()
	defer s.Stop()
	a.OnGetStatus = hook
	a.Healthy = false

	added := make(chan struct{}, 1)
	b.OnAddJob = func(*host.Job) {
		select {
		case added <- struct{}{}:
		default:
		}
	}
	cluster.RemoveHost(a.ID())
	waitSignal(c, entered, "GetStatus on unhealthy host A")

	s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 1}})
	select {
	case <-added:
	case <-time.After(time.Second):
		c.Fatal("AddJob on host B did not run while host A GetStatus was blocked")
	}
}

func (TestSuite) TestStaleJobListDoesNotResurrectStoppedJob(c *C) {
	s, _, a, b := startIdleScheduler(c, map[string]int{testJobType: 1})
	defer s.Stop()
	started := s.waitJobStart()
	c.Assert(started.JobID, Not(Equals), "")

	entered, release, hook := blockUntilRelease()
	defer release()
	a.OnListActiveJobs = hook
	b.OnListActiveJobs = hook
	s.triggerSyncJobs()
	waitSignal(c, entered, "ListActiveJobs")

	var hostJob string
	for _, h := range []*FakeHostClient{a, b} {
		jobs, err := h.ListJobs()
		c.Assert(err, IsNil)
		for id := range jobs {
			hostJob = id
			c.Assert(h.StopJob(id), IsNil)
		}
	}
	c.Assert(hostJob, Not(Equals), "")

	stopped := make(chan struct{})
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if !schedulerJobActive(s, started.ID) {
				close(stopped)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		c.Fatal("stop event was not applied while ListActiveJobs was blocked")
	}

	release()
	time.Sleep(200 * time.Millisecond)
	if schedulerJobActive(s, started.ID) {
		c.Fatal("stale ListActiveJobs resurrected a job that had already stopped")
	}
}

func schedulerJobActive(s *TestScheduler, id string) bool {
	job := s.InternalState().Jobs[id]
	if job == nil {
		return false
	}
	return job.State == JobStateStarting || job.State == JobStateRunning || job.State == JobStateStopping
}

func (TestSuite) TestAddJobConflictIsSuccess(c *C) {
	h := NewFakeHostClient(testHostID, false)
	var calls int64
	h.OnAddJob = func(*host.Job) { atomic.AddInt64(&calls, 1) }
	h.AddJobErr = httphelper.JSONError{Code: httphelper.ConflictErrorCode, Message: "job already exists"}
	cluster := newTestCluster(map[string]utils.HostClient{h.ID(): h})
	s := newTestScheduler(c, cluster, true, map[string]int{testJobType: 1})
	go s.Run()
	defer s.Stop()

	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&calls) > 1 {
			c.Fatalf("AddJob conflict was retried (%d calls); a 409 must be success", calls)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if atomic.LoadInt64(&calls) != 1 {
		c.Fatalf("AddJob calls = %d, want 1", calls)
	}
}

// missingVolumeHost fails every AddJob with the host's "required volume does
// not exist" error. The first AddJob waits until release is called.
func missingVolumeHost() (h *FakeHostClient, calls *int64, entered <-chan struct{}, release func()) {
	h = NewFakeHostClient(testHostID, false)
	h.AddJobErr = errors.New("job x required volume vol-x, but that volume does not exist")
	var n int64
	entered, release, hook := blockUntilRelease()
	h.OnAddJob = func(*host.Job) {
		atomic.AddInt64(&n, 1)
		hook()
	}
	return h, &n, entered, release
}

// waitJobState polls the loop's state until the only job of the test type is
// in one of want.
func waitJobState(c *C, s *TestScheduler, want ...JobState) *Job {
	deadline := time.Now().Add(2 * time.Second)
	var last *Job
	for time.Now().Before(deadline) {
		for _, job := range s.InternalState().Jobs {
			if job.Type != testJobType {
				continue
			}
			last = job
			for _, st := range want {
				if job.State == st {
					return job
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if last == nil {
		c.Fatalf("no %s job, want state %v", testJobType, want)
	}
	c.Fatalf("job %s state = %s, want %v", last.ID, last.State, want)
	return nil
}

// TestStartJobMissingVolumeMutatesOnLoop: a scale-down stops the job while
// AddJob is in flight. The missing-volume error that comes back after must
// not set the stopped job back to pending and place it again.
func (TestSuite) TestStartJobMissingVolumeMutatesOnLoop(c *C) {
	h, calls, entered, release := missingVolumeHost()
	defer release()
	cluster := newTestCluster(map[string]utils.HostClient{h.ID(): h})
	s := newTestScheduler(c, cluster, true, map[string]int{testJobType: 0})
	go s.Run()
	defer s.Stop()
	s.waitRectify()

	c.Assert(s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 1}}), IsNil)
	waitSignal(c, entered, "AddJob")
	c.Assert(s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 0}}), IsNil)
	waitJobState(c, s, JobStateStopping, JobStateStopped)

	release()
	// the first retry waits 500ms
	time.Sleep(1200 * time.Millisecond)
	if n := atomic.LoadInt64(calls); n != 1 {
		c.Fatalf("AddJob calls = %d, want 1: the stopped job was placed again", n)
	}
	job := waitJobState(c, s, JobStateStopping, JobStateStopped)
	c.Assert(job.HostID, Equals, testHostID)
}

// TestStartJobMissingPersistentVolumeDoesNotBlockStoppedJob: the persistent
// branch must not mark a job blocked after the loop stopped it.
func (TestSuite) TestStartJobMissingPersistentVolumeDoesNotBlockStoppedJob(c *C) {
	h, _, entered, release := missingVolumeHost()
	defer release()
	cluster := newTestCluster(map[string]utils.HostClient{h.ID(): h})
	s := newTestScheduler(c, cluster, true, map[string]int{testJobType: 0})
	cc := s.ControllerClient.(*FakeControllerClient)
	rel, err := cc.GetRelease(testReleaseID)
	c.Assert(err, IsNil)
	web := rel.Processes[testJobType]
	web.Volumes = []ct.VolumeReq{{Path: "/data"}}
	rel.Processes[testJobType] = web
	go s.Run()
	defer s.Stop()
	s.waitRectify()

	c.Assert(s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 1}}), IsNil)
	waitSignal(c, entered, "AddJob")
	c.Assert(s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 0}}), IsNil)
	waitJobState(c, s, JobStateStopping, JobStateStopped)

	release()
	time.Sleep(300 * time.Millisecond)
	job := waitJobState(c, s, JobStateStopping, JobStateStopped)
	c.Assert(job.hostError, IsNil)
}

// missingOnceHost fails the first AddJob with a missing volume error.
type missingOnceHost struct {
	*FakeHostClient
	calls int64
}

func (h *missingOnceHost) AddJob(job *host.Job) error {
	if atomic.AddInt64(&h.calls, 1) == 1 {
		return errors.New("job x required volume vol-x, but that volume does not exist")
	}
	return h.FakeHostClient.AddJob(job)
}

// TestStartJobMissingVolumeRetriesPendingJob keeps the normal path: a job
// that is still pending is reset on the loop and placed again.
func (TestSuite) TestStartJobMissingVolumeRetriesPendingJob(c *C) {
	h := &missingOnceHost{FakeHostClient: NewFakeHostClient(testHostID, false)}
	cluster := newTestCluster(map[string]utils.HostClient{h.ID(): h})
	s := newTestScheduler(c, cluster, true, map[string]int{testJobType: 1})
	go s.Run()
	defer s.Stop()
	waitJobState(c, s, JobStateStarting, JobStateRunning)
	if n := atomic.LoadInt64(&h.calls); n != 2 {
		c.Fatalf("AddJob calls = %d, want 2", n)
	}
}

// TestStartJobMissingPersistentVolumeBlocksPendingJob keeps the persistent
// guard: a pending job is blocked instead of given an empty volume.
func (TestSuite) TestStartJobMissingPersistentVolumeBlocksPendingJob(c *C) {
	h := &missingOnceHost{FakeHostClient: NewFakeHostClient(testHostID, false)}
	cluster := newTestCluster(map[string]utils.HostClient{h.ID(): h})
	s := newTestScheduler(c, cluster, true, map[string]int{testJobType: 0})
	cc := s.ControllerClient.(*FakeControllerClient)
	rel, err := cc.GetRelease(testReleaseID)
	c.Assert(err, IsNil)
	web := rel.Processes[testJobType]
	web.Volumes = []ct.VolumeReq{{Path: "/data"}}
	rel.Processes[testJobType] = web
	go s.Run()
	defer s.Stop()
	s.waitRectify()
	c.Assert(s.PutFormation(&ct.Formation{AppID: testAppID, ReleaseID: testReleaseID, Processes: map[string]int{testJobType: 1}}), IsNil)
	job := waitJobState(c, s, JobStateBlocked)
	c.Assert(job.hostError, NotNil)
	time.Sleep(700 * time.Millisecond)
	if n := atomic.LoadInt64(&h.calls); n != 1 {
		c.Fatalf("AddJob calls = %d, want 1 for a blocked job", n)
	}
}
