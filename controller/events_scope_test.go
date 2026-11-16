package main

import (
	"fmt"
	"io"
	"net/http"
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
