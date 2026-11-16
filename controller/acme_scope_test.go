package main

import (
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) TestACMEIssueTokenCanRenewButNotReadEnv(c *C) {
	const secret = "f4-acme-release-secret"
	app := s.createTestApp(c, &ct.App{Name: "f4-acme-app"})
	s.createTestRelease(c, app.ID, &ct.Release{Env: map[string]string{"SECRET": secret}})
	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "acme",
		UserEmail:  "acme@flynn.local",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeACMEIssue},
	})
	c.Assert(err, IsNil)

	code, body := s.scopedStatus(c, http.MethodGet, raw, "/managed-certificates?expiring_before=2099-01-01T00:00:00Z", "")
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, body = s.scopedStatus(c, http.MethodGet, raw, "/apps/"+app.ID+"/release", "")
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, _ = s.scopedStatus(c, http.MethodPut, raw, "/apps/"+app.ID+"/formation", `{}`)
	c.Assert(code, Equals, http.StatusForbidden)
	code, _ = s.scopedStatus(c, http.MethodPut, raw, "/acme/config", `{}`)
	c.Assert(code, Equals, http.StatusForbidden)
	code, _ = s.scopedStatus(c, http.MethodGet, raw, "/acme/config", "")
	c.Assert(code, Not(Equals), http.StatusForbidden)
}
