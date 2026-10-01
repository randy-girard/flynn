package main

import (
	"path/filepath"
	"testing"

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
