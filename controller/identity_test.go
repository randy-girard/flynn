package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *S) whoAmIAdmin(c *C, tok string) (int, bool) {
	code, body := s.scopedStatus(c, http.MethodGet, tok, "/whoami/grants", "")
	var out callerGrantsBody
	if code == http.StatusOK {
		c.Assert(json.Unmarshal([]byte(body), &out), IsNil, Commentf("body=%s", body))
	}
	return code, out.ClusterAdmin
}

func (s *S) storePAT(c *C, userID, scopes string) string {
	pat := "flynn_pat_" + random.Hex(32)
	sum := sha256.Sum256([]byte(pat))
	_, err := s.api.tenancy.CreatePAT(userID, random.UUID(), "legacy", scopes, hex.EncodeToString(sum[:]), nil)
	c.Assert(err, IsNil)
	return pat
}

// A personal access token speaks for its user. Its row must not be able to
// raise the user to cluster admin or to a service credential.
func (s *S) TestPATCannotCarryPrivilegedScopes(c *C) {
	u := s.createLoginUser(c, "core16-pat@flynn.test", "core16-pat", "pw-core16-pat", false)
	other := s.createLoginUser(c, "core16-other@flynn.test", "core16-other", "pw-core16-other", false)
	login := s.signUser(c, u.ID, nil)

	for _, scopes := range []string{
		"cluster:admin",
		"*",
		"account:quota",
		"acme:issue",
		"build:artifacts",
		"gitreceive:build",
		"user cluster:admin",
		"user,*",
		"app:read",
	} {
		body, _ := json.Marshal(ct.TokenCreate{Name: "core16", Scopes: scopes})
		code, resp := s.scopedStatus(c, http.MethodPost, login, "/tokens", string(body))
		c.Assert(code, Equals, http.StatusBadRequest, Commentf("scopes=%q", scopes))
		c.Assert(resp, Not(Matches), `(?s).*flynn_pat_.*`)
	}
	recs, err := s.api.tenancy.ListPATs(u.ID)
	c.Assert(err, IsNil)
	c.Assert(recs, HasLen, 0)

	// A plain token and a user token still work and are not admin.
	for _, scopes := range []string{"", "user"} {
		body, _ := json.Marshal(ct.TokenCreate{Name: "core16-ok", Scopes: scopes})
		code, resp := s.scopedStatus(c, http.MethodPost, login, "/tokens", string(body))
		c.Assert(code, Equals, http.StatusOK, Commentf("scopes=%q body=%s", scopes, resp))
		var rec ct.AccessTokenRecord
		c.Assert(json.Unmarshal([]byte(resp), &rec), IsNil)
		c.Assert(rec.Token, Not(Equals), "")
		code, admin := s.whoAmIAdmin(c, rec.Token)
		c.Assert(code, Equals, http.StatusOK)
		c.Assert(admin, Equals, false)
	}

	// A row written before the allowlist authenticates as the plain user.
	legacy := s.storePAT(c, u.ID, "cluster:admin * account:quota acme:issue")
	code, admin := s.whoAmIAdmin(c, legacy)
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(admin, Equals, false)
	code, _ = s.scopedStatus(c, http.MethodGet, legacy, "/cluster/audit", "")
	c.Assert(code, Equals, http.StatusForbidden)
	code, _ = s.scopedStatus(c, http.MethodPut, legacy, "/accounts/user:"+other.ID+"/quota", `{"max_apps":0}`)
	c.Assert(code, Equals, http.StatusForbidden)
	code, _ = s.scopedStatus(c, http.MethodGet, legacy, "/apps", "")
	c.Assert(code, Equals, http.StatusOK)

	// An admin's PAT is admin because of the user row, with no scope.
	admUser := s.createLoginUser(c, "core16-admin@flynn.test", "core16-admin", "pw-core16-admin", true)
	body, _ := json.Marshal(ct.TokenCreate{Name: "core16-admin"})
	code, resp := s.scopedStatus(c, http.MethodPost, s.signUser(c, admUser.ID, nil), "/tokens", string(body))
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", resp))
	var rec ct.AccessTokenRecord
	c.Assert(json.Unmarshal([]byte(resp), &rec), IsNil)
	code, admin = s.whoAmIAdmin(c, rec.Token)
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(admin, Equals, true)
	code, _ = s.scopedStatus(c, http.MethodGet, rec.Token, "/cluster/audit", "")
	c.Assert(code, Equals, http.StatusOK)

	// Demoting the admin takes admin away from the same PAT.
	admUser.ClusterAdmin = false
	c.Assert(s.api.tenancy.UpdateUser(admUser), IsNil)
	code, admin = s.whoAmIAdmin(c, rec.Token)
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(admin, Equals, false)
}

// A personal access token always expires (CORE-50). With no expires_at it
// gets the cluster default; one past the maximum is refused, in both
// tenancy modes, and the bootstrap token gets the default too.
func (s *S) TestCreateTokenAppliesMaxLifetime(c *C) {
	day := 24 * time.Hour
	u := s.createLoginUser(c, "core50-pat@flynn.test", "core50-pat", "pw-core50-pat", true)
	login := s.signUser(c, u.ID, nil)
	create := func(body ct.TokenCreate) (int, ct.AccessTokenRecord, string) {
		raw, _ := json.Marshal(body)
		code, resp := s.scopedStatus(c, http.MethodPost, login, "/tokens", string(raw))
		var rec ct.AccessTokenRecord
		if code == http.StatusOK {
			c.Assert(json.Unmarshal([]byte(resp), &rec), IsNil)
		}
		return code, rec, resp
	}
	near := func(got *time.Time, want time.Time) {
		c.Assert(got, NotNil)
		c.Assert(got.Sub(want) < time.Minute && want.Sub(*got) < time.Minute, Equals, true, Commentf("got %v want ~%v", got, want))
	}

	for _, mode := range []string{"self_hosted", "hosted"} {
		c.Assert(s.api.tenancy.SetSettings(mode, nil), IsNil)

		code, rec, resp := create(ct.TokenCreate{Name: "core50-default"})
		c.Assert(code, Equals, http.StatusOK, Commentf("mode=%s body=%s", mode, resp))
		near(rec.ExpiresAt, time.Now().Add(90*day))
		recs, err := s.api.tenancy.ListPATs(u.ID)
		c.Assert(err, IsNil)
		var stored *time.Time
		for _, r := range recs {
			if r.ID == rec.ID {
				stored = r.ExpiresAt
			}
		}
		near(stored, time.Now().Add(90*day))

		within := time.Now().Add(300 * day).UTC().Truncate(time.Second)
		code, rec, resp = create(ct.TokenCreate{Name: "core50-within", ExpiresAt: &within})
		c.Assert(code, Equals, http.StatusOK, Commentf("mode=%s body=%s", mode, resp))
		c.Assert(rec.ExpiresAt.Equal(within), Equals, true)

		for _, bad := range []time.Time{time.Now().Add(400 * day), time.Now().Add(100 * 365 * day), time.Now().Add(-time.Hour)} {
			bad := bad
			code, _, resp = create(ct.TokenCreate{Name: "core50-bad", ExpiresAt: &bad})
			c.Assert(code, Equals, http.StatusBadRequest, Commentf("mode=%s expires=%v body=%s", mode, bad, resp))
			c.Assert(resp, Matches, `(?s).*expires_at.*`)
			c.Assert(resp, Not(Matches), `(?s).*flynn_pat_.*`)
		}
	}

	// A self-hosted operator can opt out; hosted mode still caps at 365 days.
	saved := s.api.patLifetime
	defer func() { s.api.patLifetime = saved }()
	s.api.patLifetime = patLifetimePolicy{Max: patLifetimeNone, Default: patLifetimeNone}
	c.Assert(s.api.tenancy.SetSettings("self_hosted", nil), IsNil)
	code, rec, resp := create(ct.TokenCreate{Name: "core50-optout"})
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", resp))
	c.Assert(rec.ExpiresAt, IsNil)
	c.Assert(s.api.tenancy.SetSettings("hosted", nil), IsNil)
	code, rec, resp = create(ct.TokenCreate{Name: "core50-optout-hosted"})
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", resp))
	near(rec.ExpiresAt, time.Now().Add(365*day))
	c.Assert(s.api.tenancy.SetSettings("self_hosted", nil), IsNil)
	s.api.patLifetime = saved

	// flynn-host user:token goes through the same policy.
	req, err := http.NewRequest(http.MethodPost, s.srv.URL+"/users/"+u.ID+"/bootstrap-token", nil)
	c.Assert(err, IsNil)
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("", authKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, http.StatusOK)
	var boot ct.AccessTokenRecord
	c.Assert(json.NewDecoder(res.Body).Decode(&boot), IsNil)
	near(boot.ExpiresAt, time.Now().Add(90*day))
}

// A signed login token for a known user is admin only while users.cluster_admin
// is set. A token signed before a demotion keeps its claim until expiry, so
// expandToken must not trust it.
func (s *S) TestExpandTokenDerivesAdminFromUserRow(c *C) {
	u := s.createLoginUser(c, "core16-jwt@flynn.test", "core16-jwt", "pw-core16-jwt", false)
	for _, scope := range []string{"cluster:admin", "*"} {
		now := time.Now()
		tok, err := s.tokenSigner.Sign(&api.AccessToken{
			UserId:     u.ID,
			UserEmail:  u.Email,
			IssueTime:  timestamppb.New(now),
			ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
			Scopes:     []string{scope},
		})
		c.Assert(err, IsNil)
		code, admin := s.whoAmIAdmin(c, tok)
		c.Assert(code, Equals, http.StatusOK)
		c.Assert(admin, Equals, false, Commentf("scope=%q", scope))
		code, _ = s.scopedStatus(c, http.MethodGet, tok, "/cluster/audit", "")
		c.Assert(code, Equals, http.StatusForbidden, Commentf("scope=%q", scope))
	}
}
