package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/host/types"
)

func Test(t *testing.T) { TestingT(t) }

type S struct{}

var _ = Suite(&S{})

func (S) TestStateHostID(c *C) {
	workdir := c.MkDir()
	hostID := "abc123"
	state := NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()
	state.AddJob(&host.Job{ID: "a"})
	job := state.GetJob("a")
	if job.HostID != hostID {
		c.Errorf("expected job.HostID to equal %s, got %s", hostID, job.HostID)
	}
}

func (S) TestStateAddJobStampsProcessName(c *C) {
	workdir := c.MkDir()
	state := NewState("abc123", filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()
	id := "abc123-97100ccb-1b45-4af1-9c71-78e1d3571042"
	c.Assert(state.AddJob(&host.Job{
		ID: id,
		Metadata: map[string]string{
			"flynn-controller.type":     "postgres",
			"flynn-controller.app_name": "postgres",
		},
	}), IsNil)
	job := state.GetJob(id)
	if job.Job.Metadata[host.MetaControllerName] != "postgres.8676" {
		c.Fatalf("name=%q", job.Job.Metadata[host.MetaControllerName])
	}
	if job.Job.Config.Env["FLYNN_JOB_NAME"] != "postgres.8676" {
		c.Fatalf("env=%q", job.Job.Config.Env["FLYNN_JOB_NAME"])
	}
}

func (S) TestStatePersistRestore(c *C) {
	workdir := c.MkDir()
	hostID := "abc123"
	state := NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	state.AddJob(&host.Job{ID: "a"})
	state.CloseDB()

	// exercise the restore path.  failures will panic.
	// note that this does not test backend deserialization (the mock, obviously, isn't doing anything).
	state = NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()
	state.Restore(&MockBackend{}, nil)

	// check we actually got job data back
	job := state.GetJob("a")
	if job.HostID != hostID {
		c.Errorf("expected job.HostID to equal %s, got %s", hostID, job.HostID)
	}
}

type failOnRestoreBackend struct {
	MockBackend
	state *State
}

func (b *failOnRestoreBackend) UnmarshalState(jobs map[string]*host.ActiveJob, _ map[string][]byte, _ []byte, _ host.LogBuffers) error {
	for id := range jobs {
		b.state.SetStatusFailed(id, fmt.Errorf("container missing after host restore"))
	}
	return nil
}

func (S) TestRestoreUnmarshalStateOutsideBoltView(c *C) {
	workdir := c.MkDir()
	hostID := "abc123"
	state := NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	c.Assert(state.AddJob(&host.Job{ID: "a"}), IsNil)
	state.CloseDB()

	state = NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()

	done := make(chan error, 1)
	go func() {
		_, err := state.Restore(&failOnRestoreBackend{state: state}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		c.Assert(err, IsNil)
	case <-time.After(5 * time.Second):
		c.Fatal("Restore deadlocked: UnmarshalState/SetStatusFailed ran inside an open bolt View")
	}
	job := state.GetJob("a")
	if job == nil || job.Status != host.StatusFailed {
		c.Fatalf("expected job failed after restore UnmarshalState, got %+v", job)
	}
}

func (S) TestStateDuplicateID(c *C) {
	workdir := c.MkDir()
	hostID := "abc123"
	state := NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()

	c.Assert(state.AddJob(&host.Job{ID: "a"}), IsNil)
	c.Assert(state.AddJob(&host.Job{ID: "a"}), Equals, ErrJobExists)
}

func (S) TestStateExclusiveVolumes(c *C) {
	workdir := c.MkDir()
	hostID := "abc123"
	state := NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()

	addJob := func(jobID string, volIDs ...string) error {
		vols := make([]host.VolumeBinding, len(volIDs))
		for i, id := range volIDs {
			vols[i] = host.VolumeBinding{
				VolumeID: id,
				Target:   "/data",
			}
		}
		return state.AddJob(&host.Job{ID: jobID, Config: host.ContainerConfig{Volumes: vols}})
	}

	// add jobs with distinct volumes
	c.Assert(addJob("job1", "vol1"), IsNil)
	c.Assert(addJob("job2", "vol2", "vol3"), IsNil)

	// adding a job with any volume which is in use should fail
	c.Assert(addJob("job3", "vol1"), ErrorMatches, "volumes in use: vol1")
	c.Assert(addJob("job3", "vol2"), ErrorMatches, "volumes in use: vol2")
	c.Assert(addJob("job3", "vol3"), ErrorMatches, "volumes in use: vol3")
	c.Assert(addJob("job3", "vol1", "vol3"), ErrorMatches, "volumes in use: vol1, vol3")
	c.Assert(addJob("job3", "vol2", "vol3"), ErrorMatches, "volumes in use: vol2, vol3")
	c.Assert(addJob("job3", "vol1", "vol4"), ErrorMatches, "volumes in use: vol1")

	// adding a job with a volume which is no longer in use should succeed
	state.SetStatusDone("job1", 0)
	c.Assert(addJob("job3", "vol1"), IsNil)
	state.SetStatusDone("job2", 0)
	c.Assert(addJob("job4", "vol1", "vol2"), ErrorMatches, "volumes in use: vol1")
	c.Assert(addJob("job4", "vol2", "vol3"), IsNil)
}

type flakyRunBackend struct {
	MockBackend
	fails int
	runs  int
}

func (b *flakyRunBackend) Run(*host.Job, *RunConfig, *RateLimitBucket) error {
	b.runs++
	if b.runs <= b.fails {
		return fmt.Errorf("mounting root overlay: no such file or directory")
	}
	return nil
}

func (S) TestRestoreRetriesPersistentJobRun(c *C) {
	workdir := c.MkDir()
	hostID := "devnode2"
	state := NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	oldID := "devnode2-old-discoverd"
	c.Assert(state.AddJob(&host.Job{
		ID: oldID,
		Metadata: map[string]string{
			"flynn-controller.app_name": "discoverd",
			"flynn-system-app":          "true",
		},
		Config: host.ContainerConfig{
			HostNetwork: true,
			Env:         map[string]string{"FLYNN_JOB_ID": oldID, "DISCOVERD": "http://127.0.0.1:1111"},
		},
	}), IsNil)
	c.Assert(state.SetPersistentSlot("discoverd", oldID), IsNil)
	state.CloseDB()

	state = NewState(hostID, filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	defer state.CloseDB()
	backend := &flakyRunBackend{fails: 2}
	resurrect, err := state.Restore(backend, nil)
	c.Assert(err, IsNil)
	resurrect()
	if backend.runs != 3 {
		c.Fatalf("want 3 Run attempts after 2 overlay ENOENT failures, got %d", backend.runs)
	}
	var running int
	for _, job := range state.Get() {
		if job.Job != nil && job.Job.ID != oldID && job.Job.Metadata["flynn-controller.app_name"] == "discoverd" {
			running++
		}
	}
	if running < 1 {
		c.Fatal("discoverd must be resurrected locally after overlay ENOENT; scheduler is gone on the second rolling-reboot node")
	}
}
