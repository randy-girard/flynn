package main

import (
	"encoding/json"
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

// TestAccountQuotaTokenListsFormations is the billing meter's read: the
// quota token lists an app's formations, and single formations (whose
// ?expand=true embeds the release) stay closed.
func (s *S) TestAccountQuotaTokenListsFormations(c *C) {
	const secret = "core11-billing-secret"
	app := s.createTestApp(c, &ct.App{Name: "core11-billing-app", OwnerAccount: "user:core11-owner"})
	release := s.createTestRelease(c, app.ID, &ct.Release{
		Env:       map[string]string{"SECRET": secret},
		Processes: map[string]ct.ProcessType{"web": {}},
	})
	// TestFormationListActive counts every active formation in the shared
	// database, so this one is removed when the test ends.
	formation := s.createTestFormation(c, &ct.Formation{AppID: app.ID, ReleaseID: release.ID, Processes: map[string]int{"web": 2}})
	defer s.deleteTestFormation(formation)
	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "billing",
		UserEmail:  "billing@flynn.local",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeAccountQuota},
	})
	c.Assert(err, IsNil)

	code, body := s.scopedStatus(c, http.MethodGet, raw, "/apps/"+app.ID+"/formations", "")
	c.Assert(code, Equals, http.StatusOK, Commentf("body: %s", body))
	var list []*ct.Formation
	c.Assert(json.Unmarshal([]byte(body), &list), IsNil)
	c.Assert(list, HasLen, 1)
	c.Assert(list[0].ReleaseID, Equals, release.ID)
	c.Assert(list[0].Processes["web"], Equals, 2)
	c.Assert(strings.Contains(body, secret), Equals, false)

	code, body = s.scopedStatus(c, http.MethodGet, raw, "/apps/"+app.ID+"/formations/"+release.ID+"?expand=true", "")
	c.Assert(code, Equals, http.StatusForbidden)
	c.Assert(strings.Contains(body, secret), Equals, false)
	code, _ = s.scopedStatus(c, http.MethodPut, raw, "/apps/"+app.ID+"/formations/"+release.ID, `{"processes":{"web":0}}`)
	c.Assert(code, Equals, http.StatusForbidden)
}
