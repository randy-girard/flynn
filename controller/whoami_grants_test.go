package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/buildtoken"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
)

type callerGrantsBody struct {
	UserID       string `json:"user_id"`
	ClusterAdmin bool   `json:"cluster_admin"`
	AppGrants    []struct {
		AppID       string   `json:"app_id"`
		Permissions []string `json:"permissions"`
	} `json:"app_grants"`
}

// GET /whoami/grants is what gitreceive asks on a tenant's behalf: the
// caller's own expanded grants, keyed by app id. It must carry the tenant's
// own apps (owner gets deploy) and nothing of another tenant's.
func (s *S) TestWhoAmIGrantsListsOwnApps(c *C) {
	ua := s.createLoginUser(c, "new02-g-a@flynn.test", "new02-g-a", "pw-new02-g-a", false)
	ub := s.createLoginUser(c, "new02-g-b@flynn.test", "new02-g-b", "pw-new02-g-b", false)
	admin := s.createLoginUser(c, "new02-g-admin@flynn.test", "new02-g-admin", "pw-new02-g-admin", true)
	appA := s.createTestApp(c, &ct.App{Name: "new02-g-a", OwnerAccount: "user:" + ua.ID})
	appB := s.createTestApp(c, &ct.App{Name: "new02-g-b", OwnerAccount: "user:" + ub.ID})

	get := func(tok string) (int, callerGrantsBody) {
		code, body := s.scopedStatus(c, http.MethodGet, tok, "/whoami/grants", "")
		var out callerGrantsBody
		if code == http.StatusOK {
			c.Assert(json.Unmarshal([]byte(body), &out), IsNil, Commentf("body=%s", body))
		}
		return code, out
	}
	perms := func(g callerGrantsBody, appID string) []string {
		for _, ag := range g.AppGrants {
			if ag.AppID == appID {
				return ag.Permissions
			}
		}
		return nil
	}

	code, got := get(s.signUser(c, ua.ID, nil))
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(got.UserID, Equals, ua.ID)
	c.Assert(got.ClusterAdmin, Equals, false)
	c.Assert(authz.HasAppPermission(perms(got, appA.ID), authz.PermAppDeploy), Equals, true, Commentf("grants=%+v", got))
	c.Assert(perms(got, appB.ID), HasLen, 0)
	for _, ag := range got.AppGrants {
		c.Assert(ag.AppID, Not(Equals), appA.Name, Commentf("grant keyed by app name"))
	}

	// A personal access token speaks for the same user.
	pat := "flynn_pat_" + random.Hex(32)
	sum := sha256.Sum256([]byte(pat))
	_, err := s.api.tenancy.CreatePAT(ua.ID, random.UUID(), "new02", "", hex.EncodeToString(sum[:]), nil)
	c.Assert(err, IsNil)
	code, got = get(pat)
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(authz.HasAppPermission(perms(got, appA.ID), authz.PermAppDeploy), Equals, true)
	c.Assert(perms(got, appB.ID), HasLen, 0)

	code, got = get(s.signUser(c, admin.ID, nil))
	c.Assert(code, Equals, http.StatusOK)
	c.Assert(got.ClusterAdmin, Equals, true)

	// A compile credential is not the owner.
	receive, err := buildtoken.MintReceive(s.tokenSigner, appA, time.Minute)
	c.Assert(err, IsNil)
	code, _ = get(receive)
	c.Assert(code, Equals, http.StatusForbidden)

	// A suspended account gets nothing.
	c.Assert(s.api.tenancy.SetAccountSuspended("user:"+ub.ID, true), IsNil)
	code, _ = get(s.signUser(c, ub.ID, nil))
	c.Assert(code, Equals, http.StatusForbidden)
}
