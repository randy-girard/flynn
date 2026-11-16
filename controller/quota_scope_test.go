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

func (s *S) TestAccountQuotaTokenCannotReadReleaseEnv(c *C) {
	const secret = "f4-billing-other-secret"
	app := s.createTestApp(c, &ct.App{Name: "f4-billing-app", OwnerAccount: "user:f4-billing-owner"})
	s.createTestRelease(c, app.ID, &ct.Release{Env: map[string]string{"SECRET": secret}})
	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "billing",
		UserEmail:  "billing@flynn.local",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeAccountQuota},
	})
	c.Assert(err, IsNil)
	code, body := s.scopedStatus(c, http.MethodGet, raw, "/apps/"+app.ID+"/release", "")
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)
	code, _ = s.scopedStatus(c, http.MethodPut, raw, "/accounts/user:f4-billing-owner/quota", `{}`)
	c.Assert(code, Not(Equals), http.StatusForbidden)
	code, _ = s.scopedStatus(c, http.MethodPost, raw, "/accounts/user:f4-billing-owner/suspend", `{}`)
	c.Assert(code, Not(Equals), http.StatusForbidden)
	code, _ = s.scopedStatus(c, http.MethodPut, raw, "/cluster/settings", `{}`)
	c.Assert(code, Equals, http.StatusForbidden)
}
