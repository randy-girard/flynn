package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	c "github.com/flynn/go-check"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/cluster"
	sc "github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// clusterUpdateHelpers are shared by rolling-restart and sirenia recovery tests.
type clusterUpdateHelpers struct {
	t          *c.C
	x          *Cluster
	controller controller.Client
	disc       *discoverd.Client
}

func newClusterUpdateHelpers(t *c.C, x *Cluster) *clusterUpdateHelpers {
	return &clusterUpdateHelpers{
		t:          t,
		x:          x,
		controller: x.controller,
		disc:       discoverd.NewClientWithURL(fmt.Sprintf("http://%s:1111", x.IP)),
	}
}

func (h *clusterUpdateHelpers) waitClusterHosts(want int) {
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		hs, err := h.x.cluster.Hosts()
		if err == nil && len(hs) >= want {
			return
		}
		time.Sleep(2 * time.Second)
	}
	h.t.Fatalf("timed out waiting for %d hosts in discoverd", want)
}

func (h *clusterUpdateHelpers) waitHostStatus(host *cluster.Host) {
	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, lastErr = host.GetStatus(); lastErr == nil {
			return
		}
		time.Sleep(2 * time.Second)
	}
	h.t.Fatalf("host %s did not recover GetStatus: %v", host.ID(), lastErr)
}

func (h *clusterUpdateHelpers) sireniaState(service string) (*state.State, error) {
	meta, err := h.disc.Service(service).GetMeta()
	if err != nil {
		return nil, err
	}
	if meta == nil || len(meta.Data) == 0 {
		return nil, fmt.Errorf("empty %s service meta", service)
	}
	var st state.State
	if err := json.Unmarshal(meta.Data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (h *clusterUpdateHelpers) waitSireniaHA(service string, expectedPeers int, timeout time.Duration) *state.State {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		st, err := h.sireniaState(service)
		if err != nil {
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}
		if st.Primary != nil && st.Sync != nil && 2+len(st.Async) == expectedPeers {
			if status, err := sc.NewClient(st.Primary.Addr).Status(); err == nil &&
				status.Database != nil && status.Database.Running && status.Database.ReadWrite {
				return st
			}
		}
		lastErr = fmt.Errorf("primary=%v sync=%v asyncs=%d", st.Primary != nil, st.Sync != nil, len(st.Async))
		time.Sleep(2 * time.Second)
	}
	h.t.Fatalf("timed out waiting for %s HA cluster (%d peers): %v", service, expectedPeers, lastErr)
	return nil
}

func (h *clusterUpdateHelpers) waitPostgresLeader() {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		leader, err := h.disc.Service("postgres").Leader()
		if err == nil && leader != nil && leader.Addr != "" {
			if meta, err := sc.NewClient(leader.Addr).Status(); err == nil && meta != nil && meta.Database != nil {
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	h.t.Fatal("timed out waiting for postgres discoverd leader")
}

func (h *clusterUpdateHelpers) jobID(inst *discoverd.Instance) string {
	if inst == nil || inst.Meta == nil {
		return ""
	}
	return inst.Meta["FLYNN_JOB_ID"]
}

func (h *clusterUpdateHelpers) stopJob(jobID string) {
	h.t.Assert(jobID, c.Not(c.Equals), "")
	hostID, _ := cluster.ExtractHostID(jobID)
	hosts, err := h.x.cluster.Hosts()
	h.t.Assert(err, c.IsNil)
	var target *cluster.Host
	for _, host := range hosts {
		if host.ID() == hostID {
			target = host
			break
		}
	}
	h.t.Assert(target, c.NotNil)
	debugf(h.t, "stopping job %s on host %s", jobID, hostID)
	h.t.Assert(target.StopJob(jobID), c.IsNil)
}

func (h *clusterUpdateHelpers) assertAppHTTP(appName string) {
	app, err := h.controller.GetApp(appName)
	h.t.Assert(err, c.IsNil)
	routes, err := h.controller.AppRouteList(app.ID)
	h.t.Assert(err, c.IsNil)
	h.t.Assert(len(routes) > 0, c.Equals, true)
	domain := routes[0].Domain

	client := &http.Client{Timeout: 15 * time.Second}
	deadline := time.Now().Add(2 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequest("GET", "http://"+h.x.IP, nil)
		h.t.Assert(err, c.IsNil)
		req.Host = domain
		resp, err := client.Do(req)
		if err == nil {
			body, _ := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				debugf(h.t, "app ok: %s", strings.TrimSpace(string(body)))
				return
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	h.t.Fatalf("app did not respond 200: %v", lastErr)
}

// TestRollingSystemctlRestartThreeNode exercises the production update
// restart model (systemctl restart flynn-host per host; containers survive via
// KillMode=process) on a 3-node cluster. After each host restart it asserts:
//   - discoverd still lists 3 flynn-host peers
//   - the restarted host answers GetStatus
//   - postgres retains a discoverd leader and accepts reads/writes
//   - a user HTTP app keeps serving
//
// This is the multi-host analogue of TestUpdateLogs and covers the settle/
// reattach path that flynn-host update --all-nodes relies on between remotes.
func (s *HostUpdateSuite) TestRollingSystemctlRestartThreeNode(t *c.C) {
	x := s.bootCluster(t, 3)
	defer x.Destroy()

	h := newClusterUpdateHelpers(t, x)
	hosts, err := x.cluster.Hosts()
	t.Assert(err, c.IsNil)
	t.Assert(hosts, c.HasLen, 3)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID() < hosts[j].ID() })

	r := s.newGitRepo(t, "http")
	r.cluster = x
	t.Assert(r.flynn("create", "update-http"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)

	t.Assert(r.flynn("resource", "add", "postgres"), Succeeds)
	t.Assert(r.flynn("pg", "psql", "--", "-c",
		"CREATE TABLE update_probe (id serial PRIMARY KEY, data text); INSERT INTO update_probe (data) VALUES ('pre-update');"),
		Succeeds)

	assertAppAndDB := func(label string) {
		debugf(t, "post-restart checks (%s)", label)
		h.waitClusterHosts(3)
		h.waitPostgresLeader()

		query := r.flynn("pg", "psql", "--", "-c", "SELECT data FROM update_probe WHERE data = 'pre-update'")
		t.Assert(query, SuccessfulOutputContains, "pre-update")

		marker := fmt.Sprintf("after-%s-%d", label, time.Now().UnixNano())
		t.Assert(r.flynn("pg", "psql", "--", "-c",
			fmt.Sprintf("INSERT INTO update_probe (data) VALUES ('%s');", marker)),
			Succeeds)
		t.Assert(r.flynn("pg", "psql", "--", "-c",
			fmt.Sprintf("SELECT data FROM update_probe WHERE data = '%s'", marker)),
			SuccessfulOutputContains, marker)

		h.assertAppHTTP("update-http")
	}

	assertAppAndDB("baseline")

	for i, host := range hosts {
		debugf(t, "systemctl-restart host %d/%d id=%s", i+1, len(hosts), host.ID())
		t.Assert(host.SystemctlRestart(), c.IsNil)
		h.waitHostStatus(host)
		time.Sleep(15 * time.Second)
		assertAppAndDB(fmt.Sprintf("host-%d", i+1))
	}

	app, err := x.controller.GetApp("update-http")
	t.Assert(err, c.IsNil)
	release, err := x.controller.GetAppRelease(app.ID)
	t.Assert(err, c.IsNil)
	formation, err := x.controller.GetFormation(app.ID, release.ID)
	t.Assert(err, c.IsNil)
	t.Assert(formation.Processes["web"] > 0, c.Equals, true)
}

// TestRollingSystemctlRestartThreeNode_WithRestartingAsync stops the postgres
// async peer so a replacement is mid-start/reseed while hosts roll. Validates
// HA recovery with all peers Running, and that the seeding replacement is not
// torn down mid-seed during settle (the updater's RepairSireniaClusterQuorum
// path is covered by unit tests; this exercises the host-restart settle window).
func (s *HostUpdateSuite) TestRollingSystemctlRestartThreeNode_WithRestartingAsync(t *c.C) {
	x := s.bootCluster(t, 3)
	defer x.Destroy()

	h := newClusterUpdateHelpers(t, x)
	hosts, err := x.cluster.Hosts()
	t.Assert(err, c.IsNil)
	t.Assert(hosts, c.HasLen, 3)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID() < hosts[j].ID() })

	r := s.newGitRepo(t, "http")
	r.cluster = x
	t.Assert(r.flynn("create", "update-async-http"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	t.Assert(r.flynn("resource", "add", "postgres"), Succeeds)

	st := h.waitSireniaHA("postgres", 3, 5*time.Minute)
	t.Assert(len(st.Async) > 0, c.Equals, true)
	oldAsyncJobID := h.jobID(st.Async[0])
	primaryJobID := h.jobID(st.Primary)
	syncJobID := h.jobID(st.Sync)
	t.Assert(oldAsyncJobID, c.Not(c.Equals), "")
	debugf(t, "stopping postgres async job %s before rolling restart", oldAsyncJobID)
	h.stopJob(oldAsyncJobID)

	// Wait for a replacement async job (likely mid-seed / Running=false).
	var seedingJobID string
	seedDeadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(seedDeadline) {
		cur, err := h.sireniaState("postgres")
		if err == nil && cur != nil {
			for _, a := range cur.Async {
				id := h.jobID(a)
				if id != "" && id != oldAsyncJobID {
					seedingJobID = id
					break
				}
			}
		}
		if seedingJobID == "" {
			jobs, err := x.controller.JobList("postgres")
			if err == nil {
				for _, job := range jobs {
					if job.Type != "postgres" {
						continue
					}
					if job.ID == oldAsyncJobID || job.ID == primaryJobID || job.ID == syncJobID {
						continue
					}
					if job.State == ct.JobStateStarting || job.State == ct.JobStateUp {
						seedingJobID = job.ID
						break
					}
				}
			}
		}
		if seedingJobID != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	t.Assert(seedingJobID, c.Not(c.Equals), "")
	debugf(t, "tracking seeding postgres job %s during rolling restart", seedingJobID)

	deletedSeeding := false
	jobEvents := make(chan *ct.Job)
	stream, err := x.controller.StreamJobEvents("postgres", jobEvents)
	t.Assert(err, c.IsNil)
	defer stream.Close()
	go func() {
		for job := range jobEvents {
			if job != nil && job.ID == seedingJobID && job.State == ct.JobStateDown {
				deletedSeeding = true
			}
		}
	}()

	for i, host := range hosts {
		debugf(t, "systemctl-restart host %d/%d id=%s (async recovering)", i+1, len(hosts), host.ID())
		t.Assert(host.SystemctlRestart(), c.IsNil)
		h.waitHostStatus(host)
		time.Sleep(15 * time.Second)
		h.waitClusterHosts(3)
	}

	recovered := h.waitSireniaHA("postgres", 3, 10*time.Minute)
	t.Assert(recovered.Primary, c.NotNil)
	for _, peer := range append([]*discoverd.Instance{recovered.Primary, recovered.Sync}, recovered.Async...) {
		status, err := sc.NewClient(peer.Addr).Status()
		t.Assert(err, c.IsNil)
		t.Assert(status.Database, c.NotNil)
		t.Assert(status.Database.Running, c.Equals, true)
	}

	t.Assert(deletedSeeding, c.Equals, false)
	t.Assert(r.flynn("pg", "psql", "--", "-c", "SELECT 1"), Succeeds)
	h.assertAppHTTP("update-async-http")
}

// TestFlynnHostUpdateAllNodesThreeNode is reserved for a real
// `flynn-host update --all-nodes` against a local tarball. Nested cluster2
// hosts expose SystemctlRestart over the host API but not remote shell/exec,
// so this path cannot drive the CLI updater today. ReleaseSuite.TestReleaseImages
// covers GitHub-based --all-nodes updates on the release cluster.
func (s *HostUpdateSuite) TestFlynnHostUpdateAllNodesThreeNode(t *c.C) {
	if os.Getenv("FLYNN_TEST_UPDATE_TARBALL") == "" {
		t.Skip("set FLYNN_TEST_UPDATE_TARBALL when cluster hosts gain remote exec for flynn-host update; see ReleaseSuite.TestReleaseImages")
	}
	t.Skip("cluster2 has no remote exec API to run flynn-host update --all-nodes; use ReleaseSuite.TestReleaseImages")
}
