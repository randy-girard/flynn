package main

import (
	"path/filepath"

	"github.com/flynn/flynn/host/types"
	. "github.com/flynn/go-check"
)

type ResourceCheckSuite struct{}

var _ = Suite(&ResourceCheckSuite{})

func (ResourceCheckSuite) newHost(c *C) *Host {
	workdir := c.MkDir()
	state := NewState("host-test", filepath.Join(workdir, "host-state-db"))
	c.Assert(state.OpenDB(), IsNil)
	return &Host{id: "host-test", state: state}
}

// TestOwnJobPortsIncludesActiveJobPorts verifies that ports declared by the
// host's own active (starting/running) jobs are reported as owned, with a
// default proto of "tcp" applied when unset.
func (s ResourceCheckSuite) TestOwnJobPortsIncludesActiveJobPorts(c *C) {
	h := s.newHost(c)
	defer h.state.CloseDB()

	c.Assert(h.state.AddJob(&host.Job{
		ID: "job-a",
		Config: host.ContainerConfig{
			Ports: []host.Port{
				{Proto: "tcp", Port: 1111},
				{Port: 5002}, // proto defaults to tcp
				{Proto: "udp", Port: 53},
			},
		},
	}), IsNil)

	owned := h.ownJobPorts()
	if _, ok := owned["tcp:1111"]; !ok {
		c.Fatalf("expected tcp:1111 to be owned, got %v", owned)
	}
	if _, ok := owned["tcp:5002"]; !ok {
		c.Fatalf("expected tcp:5002 (default proto) to be owned, got %v", owned)
	}
	if _, ok := owned["udp:53"]; !ok {
		c.Fatalf("expected udp:53 to be owned, got %v", owned)
	}
}

// TestOwnJobPortsIgnoresDownJobs verifies that ports from jobs which are no
// longer active (done/crashed/failed) are not reported as owned, so a stale
// exited job does not mask a real port conflict.
func (s ResourceCheckSuite) TestOwnJobPortsIgnoresDownJobs(c *C) {
	h := s.newHost(c)
	defer h.state.CloseDB()

	c.Assert(h.state.AddJob(&host.Job{
		ID: "job-down",
		Config: host.ContainerConfig{
			Ports: []host.Port{{Proto: "tcp", Port: 4000}},
		},
	}), IsNil)
	// Move the job to a terminal state so it is no longer active.
	h.state.SetStatusDone("job-down", 0)

	owned := h.ownJobPorts()
	if _, ok := owned["tcp:4000"]; ok {
		c.Fatalf("expected tcp:4000 not to be owned for a done job, got %v", owned)
	}
}

// TestOwnJobPortsEmpty verifies that a host with no active jobs owns no ports.
func (s ResourceCheckSuite) TestOwnJobPortsEmpty(c *C) {
	h := s.newHost(c)
	defer h.state.CloseDB()

	owned := h.ownJobPorts()
	if len(owned) != 0 {
		c.Fatalf("expected no owned ports, got %v", owned)
	}
}

type SystemJobSuite struct{}

var _ = Suite(&SystemJobSuite{})

// TestIsSystemJob verifies that the host API auth key is scoped to internal
// jobs only: system apps (flynn-system-app metadata), the system partition, and
// build jobs are treated as system jobs, while user-pushed app jobs are not.
func (SystemJobSuite) TestIsSystemJob(c *C) {
	cases := []struct {
		name string
		job  *host.Job
		want bool
	}{
		{
			name: "system app metadata",
			job:  &host.Job{Metadata: map[string]string{"flynn-system-app": "true"}},
			want: true,
		},
		{
			name: "system partition",
			job:  &host.Job{Partition: "system"},
			want: true,
		},
		{
			// User-facing build jobs run attacker-controlled code (buildpacks,
			// Dockerfile RUN steps) and must NOT receive the host key.
			name: "slugbuilder build job (user code)",
			job:  &host.Job{Partition: "background", Metadata: map[string]string{"flynn-controller.type": "slugbuilder"}},
			want: false,
		},
		{
			name: "dockerbuilder build job (user code)",
			job:  &host.Job{Partition: "background", Metadata: map[string]string{"flynn-controller.type": "dockerbuilder"}},
			want: false,
		},
		{
			// The maintainer-run image builder launches host jobs via pkg/exec
			// and is trusted infrastructure tooling, so it keeps the key.
			name: "image builder app (trusted)",
			job:  &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "builder"}},
			want: true,
		},
		{
			name: "user app job",
			job: &host.Job{
				Partition: "user",
				Metadata: map[string]string{
					"flynn-controller.app_name": "resource-demo",
					"flynn-controller.type":     "web",
				},
			},
			want: false,
		},
		{
			name: "job with no metadata",
			job:  &host.Job{},
			want: false,
		},
	}
	for _, tc := range cases {
		if got := isSystemJob(tc.job); got != tc.want {
			c.Errorf("isSystemJob(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
