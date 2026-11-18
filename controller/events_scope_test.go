package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) eventsStatus(c *C, auth, rawURL string, basic bool) (int, string) {
	req, err := http.NewRequest(http.MethodGet, s.srv.URL+rawURL, nil)
	c.Assert(err, IsNil)
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth("", auth)
	} else {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	c.Assert(err, IsNil)
	return res.StatusCode, string(body)
}

// TestScopedEventsTokenHitsEventsRoutes is the production check for dashboard
// events. Denials are GET /events, not GET /apps/<other>.
func (s *S) TestScopedEventsTokenHitsEventsRoutes(c *C) {
	const secret = "f4-release-env-secret"
	appA := s.createTestApp(c, &ct.App{Name: "f4-events-own", OwnerAccount: "user:f4-acct-a"})
	appB := s.createTestApp(c, &ct.App{Name: "f4-events-other", OwnerAccount: "user:f4-acct-b"})
	s.createTestRelease(c, appA.ID, &ct.Release{Env: map[string]string{"SECRET": secret}})
	s.createTestRelease(c, appB.ID, &ct.Release{Env: map[string]string{"SECRET": secret + "-other"}})

	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "f4-acct-a",
		UserEmail:  "f4-acct-a@flynn.test",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeEventsRead},
		AppGrants: []*api.AppGrant{{
			AppId:       appA.ID,
			Permissions: []string{authz.ScopeEventsRead},
		}},
	})
	c.Assert(err, IsNil)

	var rejections []authz.ScopedRejection
	prev := authz.OnScopedReject
	authz.OnScopedReject = func(r authz.ScopedRejection) { rejections = append(rejections, r) }
	defer func() { authz.OnScopedReject = prev }()

	code, body := s.eventsStatus(c, raw, "/events", false)
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, body = s.eventsStatus(c, raw, "/events?app_id="+appB.ID, false)
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)

	var otherID int64
	c.Assert(s.hc.db.QueryRow(`SELECT event_id FROM events WHERE app_id = $1 ORDER BY event_id DESC LIMIT 1`, appB.ID).Scan(&otherID), IsNil)
	code, body = s.eventsStatus(c, raw, fmt.Sprintf("/events/%d", otherID), false)
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, body = s.eventsStatus(c, raw, "/events?app_id="+appA.ID+"&object_types=release", false)
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(strings.Contains(body, secret), Equals, false)
	c.Assert(strings.Contains(body, appA.ID), Equals, true)

	code, _ = s.eventsStatus(c, authKey, "/events?object_types=release&app_id="+appA.ID, true)
	c.Assert(code, Equals, http.StatusOK)

	if len(rejections) < 3 {
		c.Fatalf("expected rejections for the three denials, got %+v", rejections)
	}
	for _, r := range rejections {
		if strings.Contains(r.Scope, raw) || strings.Contains(r.Caller, raw) || strings.Contains(r.Reason, raw) || strings.Contains(r.Path, raw) {
			c.Fatalf("rejection included the token: %+v", r)
		}
		if r.Scope != authz.ScopeEventsRead || r.Caller != "f4-acct-a" {
			c.Fatalf("rejection = %+v", r)
		}
	}
}

// TestScopedEventsRedactAppReleaseAndResourceEnv: events:read does not expand
// to app:env:read, so app_release and resource events must not carry release
// or resource env to a scoped events token.
func (s *S) TestScopedEventsRedactAppReleaseAndResourceEnv(c *C) {
	const relSecret = "core09-release-env-secret"
	const resSecret = "core09-resource-env-secret"
	app := s.createTestApp(c, &ct.App{Name: "core09-events", OwnerAccount: "user:core09-acct"})
	rel := s.createTestRelease(c, app.ID, &ct.Release{Env: map[string]string{"SECRET": relSecret}})
	c.Assert(s.c.SetAppRelease(app.ID, rel.ID), IsNil)
	// Attach a resource whose provider returns env containing resSecret.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte(`{"id":"/things/core09","env":{"DB_PASS":"` + resSecret + `"}}`))
	}))
	defer srv.Close()
	p := &ct.Provider{URL: srv.URL + "/things", Name: "core09-provider"}
	c.Assert(s.c.CreateProvider(p), IsNil)
	conf := json.RawMessage(`{}`)
	res, err := s.c.ProvisionResource(&ct.ResourceReq{ProviderID: p.ID, Config: &conf, Apps: []string{app.ID}})
	c.Assert(err, IsNil)
	defer s.c.DeleteResource(p.ID, res.ID)

	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "core09-acct",
		UserEmail:  "core09-acct@flynn.test",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeEventsRead},
		AppGrants: []*api.AppGrant{{
			AppId:       app.ID,
			Permissions: []string{authz.ScopeEventsRead},
		}},
	})
	c.Assert(err, IsNil)

	for _, ot := range []string{"app_release", "resource"} {
		code, body := s.eventsStatus(c, raw, "/events?past=true&app_id="+app.ID+"&object_types="+ot, false)
		c.Assert(code, Equals, http.StatusOK)
		c.Assert(strings.Contains(body, ot), Equals, true, Commentf("no %s event listed: %s", ot, body))
		c.Assert(strings.Contains(body, relSecret), Equals, false, Commentf("%s leaked release env: %s", ot, body))
		c.Assert(strings.Contains(body, resSecret), Equals, false, Commentf("%s leaked resource env: %s", ot, body))
	}

	// Cluster key still sees env.
	code, body := s.eventsStatus(c, authKey, "/events?past=true&app_id="+app.ID+"&object_types=app_release", true)
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(strings.Contains(body, relSecret), Equals, true)
}
