package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/cluster"
	sc "github.com/flynn/flynn/pkg/sirenia/client"
	c "github.com/flynn/go-check"
)

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

	hosts, err := x.cluster.Hosts()
	t.Assert(err, c.IsNil)
	t.Assert(hosts, c.HasLen, 3)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID() < hosts[j].ID() })

	r := s.newGitRepo(t, "http")
	t.Assert(r.flynn("create", "update-http"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)

	t.Assert(r.flynn("resource", "add", "postgres"), Succeeds)
	t.Assert(r.flynn("pg", "psql", "--", "-c",
		"CREATE TABLE update_probe (id serial PRIMARY KEY, data text); INSERT INTO update_probe (data) VALUES ('pre-update');"),
		Succeeds)

	waitClusterHosts := func(want int) {
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			hs, err := x.cluster.Hosts()
			if err == nil && len(hs) >= want {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("timed out waiting for %d hosts in discoverd", want)
	}

	waitHostStatus := func(h *cluster.Host) {
		deadline := time.Now().Add(3 * time.Minute)
		var lastErr error
		for time.Now().Before(deadline) {
			if _, lastErr = h.GetStatus(); lastErr == nil {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("host %s did not recover GetStatus: %v", h.ID(), lastErr)
	}

	waitPostgresLeader := func() {
		disc := discoverd.NewClientWithURL(fmt.Sprintf("http://%s:1111", x.IP))
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			leader, err := disc.Service("postgres").Leader()
			if err == nil && leader != nil && leader.Addr != "" {
				if meta, err := sc.NewClient(leader.Addr).Status(); err == nil && meta != nil && meta.Database != nil {
					return
				}
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatal("timed out waiting for postgres discoverd leader after host restart")
	}

	assertAppHTTP := func() {
		app, err := x.controller.GetApp("update-http")
		t.Assert(err, c.IsNil)
		routes, err := x.controller.AppRouteList(app.ID)
		t.Assert(err, c.IsNil)
		t.Assert(len(routes) > 0, c.Equals, true)
		domain := routes[0].Domain

		client := &http.Client{Timeout: 15 * time.Second}
		deadline := time.Now().Add(2 * time.Minute)
		var lastErr error
		for time.Now().Before(deadline) {
			req, err := http.NewRequest("GET", "http://"+x.IP, nil)
			t.Assert(err, c.IsNil)
			req.Host = domain
			resp, err := client.Do(req)
			if err == nil {
				body, _ := ioutil.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					debugf(t, "app ok: %s", strings.TrimSpace(string(body)))
					return
				}
				lastErr = fmt.Errorf("status %d", resp.StatusCode)
			} else {
				lastErr = err
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("app did not respond 200: %v", lastErr)
	}

	assertAppAndDB := func(label string) {
		debugf(t, "post-restart checks (%s)", label)
		waitClusterHosts(3)
		waitPostgresLeader()

		query := r.flynn("pg", "psql", "--", "-c", "SELECT data FROM update_probe WHERE data = 'pre-update'")
		t.Assert(query, SuccessfulOutputContains, "pre-update")

		marker := fmt.Sprintf("after-%s-%d", label, time.Now().UnixNano())
		t.Assert(r.flynn("pg", "psql", "--", "-c",
			fmt.Sprintf("INSERT INTO update_probe (data) VALUES ('%s');", marker)),
			Succeeds)
		t.Assert(r.flynn("pg", "psql", "--", "-c",
			fmt.Sprintf("SELECT data FROM update_probe WHERE data = '%s'", marker)),
			SuccessfulOutputContains, marker)

		assertAppHTTP()
	}

	assertAppAndDB("baseline")

	for i, h := range hosts {
		debugf(t, "systemctl-restart host %d/%d id=%s", i+1, len(hosts), h.ID())
		t.Assert(h.SystemctlRestart(), c.IsNil)
		waitHostStatus(h)
		// Match updater inter-host intent: let scheduler/discoverd settle.
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
