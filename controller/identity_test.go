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
