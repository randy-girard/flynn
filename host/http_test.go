package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/flynn/go-check"
	"github.com/inconshreveable/log15"
	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/host/types"
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
			job:  &host.Job{Metadata: map[string]string{"flynn-system-app": "true", "flynn-controller.app_name": "builder"}},
			want: true,
		},
		{
			name: "tenant app named builder (untrusted)",
			job:  &host.Job{Metadata: map[string]string{"flynn-controller.app_name": "builder"}},
			want: false,
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

func postAddJob(t *testing.T, api *jobAPI, job *host.Job) int {
	t.Helper()
	body, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/host/jobs/"+job.ID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	api.AddJob(rr, req, httprouter.Params{{Key: "id", Value: job.ID}})
	if rr.Code != 200 {
		t.Logf("%s: %d %s", job.ID, rr.Code, rr.Body.String())
	}
	return rr.Code
}

func stateDBUsers(s *State) int {
	s.dbCond.L.Lock()
	defer s.dbCond.L.Unlock()
	return s.dbUsers
}

// TestAddJobConflictReleasesStateDB: every AddJob that the state rejects must
// release its state DB reference, or CloseDB (run by flynn-host update and
// shutdown) waits forever.
func TestAddJobConflictReleasesStateDB(t *testing.T) {
	state := NewState("host-test", filepath.Join(t.TempDir(), "host-state-db"))
	if err := state.OpenDB(); err != nil {
		t.Fatal(err)
	}
	h := &Host{id: "host-test", state: state, backend: MockBackend{}, log: log15.New()}
	h.log.SetHandler(log15.DiscardHandler())
	api := &jobAPI{host: h, addJobRateLimitBucket: NewRateLimitBucket(10)}

	vol := []host.VolumeBinding{{Target: "/data", VolumeID: "b43681a9-835e-41f5-80e1-30d457eeaca6"}}
	newJob := func(id string) *host.Job {
		return &host.Job{
			ID:         id,
			Mountspecs: []*host.Mountspec{{}},
			Config:     host.ContainerConfig{Volumes: vol},
		}
	}

	if code := postAddJob(t, api, newJob("job-a")); code != 200 {
		t.Fatalf("first add: status %d", code)
	}
	// A repeat of the same job is a 409.
	if code := postAddJob(t, api, newJob("job-a")); code != 409 {
		t.Fatalf("duplicate add: status %d, want 409", code)
	}
	// A second job on the volume job-a is using fails with ErrVolumesInUse.
	if code := postAddJob(t, api, newJob("job-b")); code < 400 {
		t.Fatalf("volume in use: status %d, want an error", code)
	}
	// A job rejected by validation, before the state is touched.
	bad := newJob("job-c")
	bad.Mountspecs = nil
	if code := postAddJob(t, api, bad); code != 400 {
		t.Fatalf("no mountspecs: status %d, want 400", code)
	}

	// The accepted job's runner goroutine releases its own reference.
	deadline := time.Now().Add(time.Second)
	for stateDBUsers(state) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := stateDBUsers(state); n != 0 {
		t.Fatalf("state DB users after rejected jobs = %d, want 0", n)
	}

	done := make(chan error, 1)
	go func() { done <- state.CloseDB() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseDB still waiting on a reference left by a rejected AddJob")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestCloseDBLogsWhileWaiting: a CloseDB held up by an open reference says so
// in the log, then returns once the reference is released.
func TestCloseDBLogsWhileWaiting(t *testing.T) {
	state := NewState("host-test", filepath.Join(t.TempDir(), "host-state-db"))
	if err := state.OpenDB(); err != nil {
		t.Fatal(err)
	}
	if err := state.Acquire(); err != nil {
		t.Fatal(err)
	}
	oldInterval := closeDBWaitLogInterval
	closeDBWaitLogInterval = 10 * time.Millisecond
	defer func() { closeDBWaitLogInterval = oldInterval }()
	var buf syncBuffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	done := make(chan error, 1)
	go func() { done <- state.CloseDB() }()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "still waiting for 1 user(s)") {
		if time.Now().After(deadline) {
			t.Fatalf("no wait log line, got %q", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("CloseDB returned while a reference was held")
	default:
	}
	state.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseDB did not return after Release")
	}
}
