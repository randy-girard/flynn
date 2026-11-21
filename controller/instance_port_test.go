package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/instanceport"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/random"
)

// instancePortProvider is a provider served by an installed plugin whose
// add-on asks for "instance_port", the way the redis, mysql and mongodb
// plugins do. Every provision gets a new external id.
func (s *S) instancePortProvider(c *C, tag string) (*ct.Provider, func()) {
	typ := "c39-" + strings.ToLower(tag)
	spec := fmt.Sprintf(`{"type":%q,"backend":"resource","verbs":["create","destroy"],"instance_port":true}`, typ)
	s.createTestApp(c, &ct.App{Name: typ + "-plugin", Meta: map[string]string{
		"flynn-plugin":         "true",
		plugin.MetaPluginAddon: spec,
	}})
	var n int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		fmt.Fprintf(w, `{"id":"/things/%s-%d","env":{"FOO_URL":"redis://leader.%s.discoverd:6379"}}`, typ, atomic.AddInt64(&n, 1), typ)
	}))
	p := s.createTestProvider(c, &ct.Provider{URL: fmt.Sprintf("http://%s/things", srv.Listener.Addr()), Name: typ})
	return p, srv.Close
}

func (s *S) provisionAt(c *C, base, providerID string) (int, *ct.Resource, string) {
	req, err := http.NewRequest("POST", base+"/providers/"+providerID+"/resources", strings.NewReader(`{}`))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	var body json.RawMessage
	json.NewDecoder(res.Body).Decode(&body)
	if res.StatusCode != 200 {
		return res.StatusCode, nil, string(body)
	}
	out := &ct.Resource{}
	c.Assert(json.Unmarshal(body, out), IsNil)
	return res.StatusCode, out, string(body)
}

func instancePortOf(c *C, r *ct.Resource) int {
	port, err := strconv.Atoi(r.Env[instanceport.EnvPort])
	c.Assert(err, IsNil, Commentf("env=%v", r.Env))
	return port
}

func (s *S) deleteResources(c *C, list []*ct.Resource) {
	for _, r := range list {
		if r != nil {
			_, err := s.c.DeleteResource(r.ProviderID, r.ID)
			c.Assert(err, IsNil)
		}
	}
}

// slowResourceInserts holds every resources INSERT open for d seconds, so
// every provision picks its port before any insert commits, as two
// controller replicas (or one busy replica) would.
func (s *S) slowResourceInserts(c *C, seconds string) func() {
	c.Assert(s.hc.db.Exec(`CREATE OR REPLACE FUNCTION c39_slow_insert() RETURNS trigger AS $$
BEGIN PERFORM pg_sleep(`+seconds+`); RETURN NEW; END $$ LANGUAGE plpgsql`), IsNil)
	c.Assert(s.hc.db.Exec(`CREATE TRIGGER c39_slow_insert BEFORE INSERT ON resources FOR EACH ROW EXECUTE PROCEDURE c39_slow_insert()`), IsNil)
	return func() {
		c.Assert(s.hc.db.Exec(`DROP TRIGGER IF EXISTS c39_slow_insert ON resources`), IsNil)
	}
}

// Two provisions that both pick before either inserts used to get the same
// FLYNN_INSTANCE_PORT (CORE-39): the lock was process local and released
// before the insert. Provisions through two controllers on one database
// must get distinct ports.
func (s *S) TestConcurrentProvisionGetsDistinctInstancePorts(c *C) {
	p, done := s.instancePortProvider(c, random.String(6))
	defer done()
	replica := s.newCountingServer(c, 5)
	defer replica.close()
	undo := s.slowResourceInserts(c, "0.3")

	const n = 8
	bases := []string{s.srv.URL, replica.url}
	results := make([]*ct.Resource, n)
	codes := make([]int, n)
	bodies := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], results[i], bodies[i] = s.provisionAt(c, bases[i%2], p.ID)
		}(i)
	}
	wg.Wait()
	undo()
	defer s.deleteResources(c, results)

	seen := map[int]string{}
	for i := 0; i < n; i++ {
		c.Assert(codes[i], Equals, 200, Commentf("provision %d: %s", i, bodies[i]))
		port := instancePortOf(c, results[i])
		c.Assert(port >= instanceport.MinPort && port <= instanceport.MaxPort, Equals, true, Commentf("port %d", port))
		if other, dup := seen[port]; dup {
			c.Fatalf("resources %s and %s both got instance port %d", other, results[i].ID, port)
		}
		seen[port] = results[i].ID
	}
}

// Finding a free port read every resource row and its env (CORE-34-F2). It
// reads only the allocated ports now, whatever else the cluster holds.
func (s *S) TestStampInstancePortReadsOnlyAllocatedPorts(c *C) {
	tag := random.String(6)
	other, otherCalls, otherDone := s.countingProvider(c, "c39-other-"+strings.ToLower(tag))
	defer otherDone()
	var filler []*ct.Resource
	for i := 0; i < 10; i++ {
		code, r, body := s.provisionAt(c, s.srv.URL, other.ID)
		c.Assert(code, Equals, 200, Commentf(body))
		filler = append(filler, r)
	}
	c.Assert(*otherCalls, Equals, 10)
	defer s.deleteResources(c, filler)

	p, done := s.instancePortProvider(c, tag)
	defer done()
	code, first, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	defer s.deleteResources(c, []*ct.Resource{first})

	cs := s.newCountingServer(c, 5)
	defer cs.close()
	code, body, _ = cs.do(c, "POST", "/providers/"+p.ID+"/resources", authKey, `{}`)
	c.Assert(code, Equals, 200, Commentf(body))
	var second ct.Resource
	c.Assert(json.Unmarshal([]byte(body), &second), IsNil)
	defer s.deleteResources(c, []*ct.Resource{&second})
	c.Assert(instancePortOf(c, &second), Not(Equals), instancePortOf(c, first))

	c.Assert(cs.counter.rowsFor("resource_list"), HasLen, 0, Commentf("provision listed every resource"))
	reads := cs.counter.rowsFor("resource_instance_port_list")
	c.Assert(reads, HasLen, 1)
	var allocated int
	c.Assert(s.hc.db.QueryRow(`SELECT COUNT(*) FROM resources WHERE deleted_at IS NULL AND instance_port IS NOT NULL`).Scan(&allocated), IsNil)
	c.Assert(reads[0], Equals, allocated-1)
}

// Deleting a resource frees its port for the next provision.
func (s *S) TestDeletedResourceReleasesInstancePort(c *C) {
	p, done := s.instancePortProvider(c, random.String(6))
	defer done()
	code, a, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	port := instancePortOf(c, a)
	s.deleteResources(c, []*ct.Resource{a})

	code, b, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	defer s.deleteResources(c, []*ct.Resource{b})
	c.Assert(instancePortOf(c, b), Equals, port)
}

// A PUT that gives a resource another live resource's port is refused, and
// a PUT that keeps a resource's own port (password rotation by a plugin)
// still works.
func (s *S) TestPutResourceRefusesAnotherResourcesInstancePort(c *C) {
	p, done := s.instancePortProvider(c, random.String(6))
	defer done()
	code, a, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	code, b, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	defer s.deleteResources(c, []*ct.Resource{a, b})

	b.Env["PASSWORD"] = "rotated"
	c.Assert(s.c.PutResource(b), IsNil)

	b.Env[instanceport.EnvPort] = a.Env[instanceport.EnvPort]
	err := s.c.PutResource(b)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*instance port.*")
	got, err := s.c.GetResource(p.ID, b.ID)
	c.Assert(err, IsNil)
	c.Assert(got.Env[instanceport.EnvPort], Not(Equals), a.Env[instanceport.EnvPort])
}

// A port two resources already shared before allocation was atomic stays
// taken until the last of them is deleted: the next provision never gets it.
func (s *S) TestLegacyDuplicateInstancePortStaysTakenAfterHolderIsDeleted(c *C) {
	p, done := s.instancePortProvider(c, random.String(6))
	defer done()
	code, holder, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	port := instancePortOf(c, holder)

	// A row that got the same port before the fix: env has the port, the
	// allocation column does not (migration 97 keeps the oldest).
	dupID := random.UUID()
	env, _ := json.Marshal(map[string]string{instanceport.EnvPort: strconv.Itoa(port), instanceport.EnvID: "legacy-" + dupID})
	c.Assert(s.hc.db.Exec(`INSERT INTO resources (resource_id, provider_id, external_id, env) VALUES ($1, $2, $3, $4)`,
		dupID, p.ID, "legacy-"+dupID, string(env)), IsNil)
	dup := &ct.Resource{ID: dupID, ProviderID: p.ID}
	defer s.deleteResources(c, []*ct.Resource{dup})

	// A plugin PUT on the legacy row keeps working.
	got, err := s.c.GetResource(p.ID, dupID)
	c.Assert(err, IsNil)
	got.Env["PASSWORD"] = "rotated"
	c.Assert(s.c.PutResource(got), IsNil)

	s.deleteResources(c, []*ct.Resource{holder})
	code, next, body := s.provisionAt(c, s.srv.URL, p.ID)
	c.Assert(code, Equals, 200, Commentf(body))
	defer s.deleteResources(c, []*ct.Resource{next})
	c.Assert(instancePortOf(c, next), Not(Equals), port)
}
