package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/router/testutils"
	router "github.com/randy-girard/flynn/router/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) setRouteCreatedAt(c *C, route *router.Route, at time.Time) {
	table := "http_routes"
	if route.Type == "tcp" {
		table = "tcp_routes"
	}
	c.Assert(s.hc.db.Exec(`UPDATE `+table+` SET created_at = $1 WHERE id = $2::uuid`, at, route.ID), IsNil)
	route.CreatedAt = at
}

func (s *S) getRoutes(c *C, auth string, basic bool, rawQuery string) (int, []*router.Route) {
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+"/routes?"+rawQuery, nil)
	c.Assert(err, IsNil)
	if basic {
		req.SetBasicAuth("", auth)
	} else if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil
	}
	var routes []*router.Route
	c.Assert(json.Unmarshal(body, &routes), IsNil)
	return res.StatusCode, routes
}

func routeIDs(routes []*router.Route) []string {
	out := make([]string, 0, len(routes))
	for _, route := range routes {
		if route != nil {
			out = append(out, route.Type+"/"+route.ID)
		}
	}
	return out
}

// TestRouteListPage is the production list: opt-in pages, visibility before
// the limit, and private keys stripped for a user token. Tenancy mode is not
// involved. Omitted count stays one array.
func (s *S) TestRouteListPage(c *C) {
	const (
		userID  = "c5-route-user"
		otherID = "c5-route-other"
	)
	ownNewApp := s.createTestApp(c, &ct.App{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-000000000010", Name: "c5-own-new", OwnerAccount: "user:" + userID,
	})
	ownOldApp := s.createTestApp(c, &ct.App{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-000000000011", Name: "c5-own-old", OwnerAccount: "user:" + userID,
	})
	otherApp := s.createTestApp(c, &ct.App{
		ID: "bbbbbbbb-bbbb-4bbb-8bbb-000000000012", Name: "c5-other-route", OwnerAccount: "user:" + otherID,
	})
	cert := testutils.TLSConfigForDomain("c5-own-new.example.com")
	ownNew := s.createTestRoute(c, ownNewApp.ID, (&router.HTTPRoute{
		Domain:  "c5-own-new.example.com",
		Service: "web",
		Certificate: &router.Certificate{
			Cert: cert.Cert,
			Key:  cert.PrivateKey,
		},
	}).ToRoute())
	ownOld := s.createTestRoute(c, ownOldApp.ID, (&router.HTTPRoute{
		Domain:  "c5-own-old.example.com",
		Service: "web",
	}).ToRoute())
	foreign := s.createTestRoute(c, otherApp.ID, (&router.HTTPRoute{
		Domain:  "c5-foreign.example.com",
		Service: "web",
	}).ToRoute())
	tieHigh := s.createTestRoute(c, ownNewApp.ID, (&router.HTTPRoute{
		Domain:  "c5-tie-b.example.com",
		Service: "web",
	}).ToRoute())
	tieLow := s.createTestRoute(c, ownNewApp.ID, (&router.HTTPRoute{
		Domain:  "c5-tie-a.example.com",
		Service: "web",
	}).ToRoute())
	// Force ids so the text tie-break is obvious. CreateRoute assigns ids;
	// the keyset compares those ids, so set timestamps and walk what we got
	// after sorting the pair ourselves when they share a timestamp.
	newAt := time.Date(2999, 1, 3, 0, 0, 0, 0, time.UTC)
	midAt := time.Date(2999, 1, 2, 0, 0, 0, 0, time.UTC)
	oldAt := time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
	tieAt := time.Date(2998, 6, 1, 0, 0, 0, 0, time.UTC)
	s.setRouteCreatedAt(c, ownNew, newAt)
	s.setRouteCreatedAt(c, foreign, midAt)
	s.setRouteCreatedAt(c, ownOld, oldAt)
	s.setRouteCreatedAt(c, tieHigh, tieAt)
	s.setRouteCreatedAt(c, tieLow, tieAt)

	status, unpaged := s.getRoutes(c, authKey, true, "")
	c.Assert(status, Equals, http.StatusOK)
	got := map[string]bool{}
	for _, id := range routeIDs(unpaged) {
		got[id] = true
	}
	for _, route := range []*router.Route{ownNew, ownOld, foreign, tieHigh, tieLow} {
		if !got[route.Type+"/"+route.ID] {
			c.Fatalf("unpaged list missing %s", route.ID)
		}
	}

	status, bad := s.getRoutes(c, authKey, true, "count=nope")
	c.Assert(status, Equals, http.StatusBadRequest)
	c.Assert(bad, IsNil)

	user := s.signUser(c, userID, nil)
	status, first := s.getRoutes(c, user, false, "count=1")
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(first, HasLen, 1)
	c.Assert(first[0].ID, Equals, ownNew.ID)
	body, err := json.Marshal(first[0])
	c.Assert(err, IsNil)
	if first[0].Certificate == nil || first[0].Certificate.Key != "" || strings.Contains(string(body), cert.PrivateKey) {
		c.Fatalf("user page leaked the private key: %#v", first[0].Certificate)
	}
	cursor := "count=1&before=" + first[0].CreatedAt.UTC().Format(time.RFC3339Nano) + "&before_id=" + first[0].Type + "/" + first[0].ID
	status, second := s.getRoutes(c, user, false, cursor)
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(second, HasLen, 1)
	c.Assert(second[0].ID, Equals, ownOld.ID)
	cursor = "count=1&before=" + second[0].CreatedAt.UTC().Format(time.RFC3339Nano) + "&before_id=" + second[0].Type + "/" + second[0].ID
	status, third := s.getRoutes(c, user, false, cursor)
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(third, HasLen, 1)
	nextTie := tieHigh
	if tieLow.ID > tieHigh.ID {
		nextTie = tieLow
	}
	c.Assert(third[0].ID, Equals, nextTie.ID)
	c.Assert(third[0].ID, Not(Equals), foreign.ID)

	// Equal timestamps: both tie routes, higher id text first, then the other, no duplicate.
	hi, lo := tieHigh, tieLow
	if hi.ID < lo.ID {
		hi, lo = lo, hi
	}
	status, tiePage := s.getRoutes(c, authKey, true, "count=1&before="+tieAt.Add(time.Second).UTC().Format(time.RFC3339Nano))
	c.Assert(status, Equals, http.StatusOK)
	// before without before_id is created_at < cursor, so both tie rows are eligible
	// and whatever else is older. The newest of that set in 2998 may be a tie row
	// only if nothing sits between tieAt+1s and the tie rows. Walk until both appear.
	seen := map[string]int{}
	var order []string
	page := tiePage
	raw := "count=1&before=" + tieAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
	for i := 0; i < 6 && len(order) < 2; i++ {
		status, page = s.getRoutes(c, authKey, true, raw)
		c.Assert(status, Equals, http.StatusOK)
		if len(page) == 0 {
			break
		}
		id := page[0].ID
		seen[id]++
		if id == hi.ID || id == lo.ID {
			order = append(order, id)
		}
		raw = "count=1&before=" + page[0].CreatedAt.UTC().Format(time.RFC3339Nano) + "&before_id=" + page[0].Type + "/" + page[0].ID
	}
	c.Assert(order, DeepEquals, []string{hi.ID, lo.ID})
	c.Assert(seen[hi.ID], Equals, 1)
	c.Assert(seen[lo.ID], Equals, 1)

	status, keyed := s.getRoutes(c, authKey, true, "count=1&before="+newAt.UTC().Format(time.RFC3339Nano)+"&before_id="+ownNew.Type+"/"+ownNew.ID)
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(keyed, HasLen, 1)
	c.Assert(keyed[0].ID, Equals, foreign.ID)
	if keyed[0].Certificate != nil && keyed[0].Certificate.Key == "" && ownNew.Certificate != nil {
		// foreign has no key; the cluster key must still return ownNew's key on its page.
	}
	// Postgres timestamptz is microseconds. A one-nanosecond cursor rounds
	// back onto ownNew and drops it. One microsecond stays strictly after.
	status, withKey := s.getRoutes(c, authKey, true, "count=1&before="+newAt.Add(time.Microsecond).UTC().Format(time.RFC3339Nano))
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(withKey, HasLen, 1)
	c.Assert(withKey[0].ID, Equals, ownNew.ID)
	c.Assert(withKey[0].Certificate, Not(IsNil))
	c.Assert(withKey[0].Certificate.Key, Equals, cert.PrivateKey)
}

func (s *S) getActiveJobs(c *C, auth string, basic bool, rawQuery string) (int, []*ct.Job) {
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+"/active-jobs?"+rawQuery, nil)
	c.Assert(err, IsNil)
	if basic {
		req.SetBasicAuth("", auth)
	} else if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	if res.StatusCode != http.StatusOK {
		return res.StatusCode, nil
	}
	var jobs []*ct.Job
	c.Assert(json.Unmarshal(body, &jobs), IsNil)
	return res.StatusCode, jobs
}

func (s *S) setJobCreatedAt(c *C, id string, at time.Time) {
	c.Assert(s.hc.db.Exec(`UPDATE job_cache SET created_at = $1 WHERE job_id = $2::uuid`, at, id), IsNil)
}

// TestActiveJobListPage pages on created_at, not updated_at, and drops
// slugrunner before the limit for a cluster-admin JWT. The cluster key still
// sees the runner. Tenancy mode is not involved.
func (s *S) TestActiveJobListPage(c *C) {
	c.Assert(s.hc.db.Exec(`UPDATE job_cache SET state = 'down'`), IsNil)
	app := s.createTestApp(c, &ct.App{Name: "c5-active-jobs"})
	release := s.createTestRelease(c, app.ID, &ct.Release{})

	makeJob := func(id, typ string, at time.Time) *ct.Job {
		job := s.createTestJob(c, &ct.Job{
			UUID:      id,
			AppID:     app.ID,
			ReleaseID: release.ID,
			Type:      typ,
			State:     ct.JobStateUp,
		})
		s.setJobCreatedAt(c, id, at)
		return job
	}
	high := makeJob("cccccccc-cccc-4ccc-8ccc-000000000003", "web", time.Date(2999, 2, 3, 0, 0, 0, 0, time.UTC))
	runner := makeJob("cccccccc-cccc-4ccc-8ccc-000000000002", "slugrunner", time.Date(2999, 2, 2, 0, 0, 0, 0, time.UTC))
	low := makeJob("cccccccc-cccc-4ccc-8ccc-000000000001", "web", time.Date(2999, 2, 1, 0, 0, 0, 0, time.UTC))
	down := makeJob("cccccccc-cccc-4ccc-8ccc-000000000004", "web", time.Date(2999, 2, 4, 0, 0, 0, 0, time.UTC))
	c.Assert(s.hc.db.Exec(`UPDATE job_cache SET state = 'down' WHERE job_id = $1::uuid`, down.UUID), IsNil)
	same := time.Date(2997, 1, 1, 0, 0, 0, 0, time.UTC)
	tieHi := makeJob("cccccccc-cccc-4ccc-8ccc-00000000000b", "web", same)
	tieLo := makeJob("cccccccc-cccc-4ccc-8ccc-00000000000a", "web", same)

	status, all := s.getActiveJobs(c, authKey, true, "")
	c.Assert(status, Equals, http.StatusOK)
	ids := map[string]bool{}
	for _, job := range all {
		ids[job.UUID] = true
	}
	c.Assert(ids[high.UUID], Equals, true)
	c.Assert(ids[runner.UUID], Equals, true)
	c.Assert(ids[low.UUID], Equals, true)
	c.Assert(ids[down.UUID], Equals, false)

	status, page := s.getActiveJobs(c, authKey, true, "count=1")
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(page, HasLen, 1)
	c.Assert(page[0].UUID, Equals, high.UUID)
	next := "count=1&before=" + page[0].CreatedAt.UTC().Format(time.RFC3339Nano) + "&before_id=" + page[0].UUID
	status, page = s.getActiveJobs(c, authKey, true, next)
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(page, HasLen, 1)
	c.Assert(page[0].UUID, Equals, runner.UUID)

	now := time.Now()
	tok, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "c5-admin",
		UserEmail:  "c5-admin@flynn.test",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{"cluster:admin"},
	})
	c.Assert(err, IsNil)
	status, page = s.getActiveJobs(c, tok, false, "count=1")
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(page, HasLen, 1)
	c.Assert(page[0].UUID, Equals, high.UUID)
	next = "count=1&before=" + page[0].CreatedAt.UTC().Format(time.RFC3339Nano) + "&before_id=" + page[0].UUID
	status, page = s.getActiveJobs(c, tok, false, next)
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(page, HasLen, 1)
	c.Assert(page[0].UUID, Equals, low.UUID)
	for _, job := range page {
		c.Assert(job.Type, Not(Equals), "slugrunner")
	}

	status, tie := s.getActiveJobs(c, authKey, true, "count=1&before="+same.Add(time.Second).UTC().Format(time.RFC3339Nano))
	c.Assert(status, Equals, http.StatusOK)
	// Jobs newer than `same` (high, runner, low) are excluded by before.
	// The first row at `same` is the higher uuid.
	c.Assert(tie, HasLen, 1)
	c.Assert(tie[0].UUID, Equals, tieHi.UUID)
	next = "count=1&before=" + tie[0].CreatedAt.UTC().Format(time.RFC3339Nano) + "&before_id=" + tie[0].UUID
	status, tie = s.getActiveJobs(c, authKey, true, next)
	c.Assert(status, Equals, http.StatusOK)
	c.Assert(tie, HasLen, 1)
	c.Assert(tie[0].UUID, Equals, tieLo.UUID)
}
