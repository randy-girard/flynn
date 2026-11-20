package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

func (s *S) putTenancyWithToken(c *C, tok *authorizer.Token, body string) *httptest.ResponseRecorder {
	req, err := http.NewRequest(http.MethodPut, "/tenancy", strings.NewReader(body))
	c.Assert(err, IsNil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	ctx := context.WithValue(context.Background(), authz.TokenContextKey, tok)
	s.api.PutTenancy(ctx, rec, req)
	return rec
}

func (s *S) auditActionsSince(c *C, start int64, action string) []*ct.AuditEntry {
	list, err := s.api.tenancy.ListAudit(start, 1000)
	c.Assert(err, IsNil)
	var out []*ct.AuditEntry
	for _, e := range list {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

// TestPutTenancyForceSelfHosted: {"mode":"self_hosted","force":true} is the
// operator override for the hosted → self_hosted refuse. Only the cluster key
// may send it. It is recorded in core_audit as tenancy.force_self_hosted with
// the number of owned apps and what the switch does. It does not clear
// owner_account, quota rows, or reserve_resources. Without force the refuse
// is unchanged.
func (s *S) TestPutTenancyForceSelfHosted(c *C) {
	defer s.restoreTenancyDefaults(c)
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	c.Assert(s.hc.db.Exec(`UPDATE runtime_settings SET reserve_resources = true WHERE id = 1`), IsNil)

	kept := 2
	c.Assert(s.api.tenancy.SetQuota(&ct.AccountQuota{Account: "user:force-kept", MaxApps: &kept}), IsNil)

	suffix := strings.ReplaceAll(random.UUID(), "-", "")[:10]
	customer := s.createTestApp(c, &ct.App{
		Name:         "force-cust-" + suffix,
		OwnerAccount: "user:force-customer",
	})

	var start int64
	c.Assert(s.hc.db.QueryRow(`SELECT COALESCE(max(id), 0) FROM core_audit`).Scan(&start), IsNil)

	// Default unchanged: no force is still refused, and the error names the
	// override.
	code, apiErr, _ := s.putTenancyRaw(c, `{"mode":"self_hosted"}`)
	c.Assert(code, Equals, 400)
	c.Assert(apiErr.Message, Matches, ".*not a safe reversal.*")
	c.Assert(apiErr.Message, Matches, ".*force.*")

	// force=false is the default.
	code, _, _ = s.putTenancyRaw(c, `{"mode":"self_hosted","force":false}`)
	c.Assert(code, Equals, 400)

	// A cluster admin user and a plain user may not force, even though a
	// cluster admin may otherwise PUT /tenancy.
	for _, tok := range []*authorizer.Token{
		{UserID: "u-admin", Scopes: []string{"cluster:admin"}},
		{UserID: "u-plain"},
		{AppGrants: []authorizer.AppGrant{{AppID: customer.ID, Permissions: []string{"app:write"}}}},
	} {
		rec := s.putTenancyWithToken(c, tok, `{"mode":"self_hosted","force":true}`)
		c.Assert(rec.Code, Equals, 403, Commentf("token %+v body %s", tok, rec.Body.String()))
		c.Assert(rec.Body.String(), Matches, "(?s).*cluster key.*")
	}

	// force only applies to self_hosted.
	code, apiErr, _ = s.putTenancyRaw(c, `{"mode":"hosted","force":true}`)
	c.Assert(code, Equals, 400)
	c.Assert(apiErr.Code, Equals, hh.ValidationErrorCode)
	c.Assert(apiErr.Message, Matches, ".*force.*self_hosted.*")

	stored, err := s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(stored.Mode, Equals, tenancy.ModeHosted)
	c.Assert(s.auditActionsSince(c, start, "tenancy.force_self_hosted"), HasLen, 0)

	// The cluster key may force.
	code, _, settings := s.putTenancyRaw(c, `{"mode":"self_hosted","force":true}`)
	c.Assert(code, Equals, 200)
	c.Assert(settings.Mode, Equals, tenancy.ModeSelfHosted)

	stored, err = s.api.tenancy.GetSettings()
	c.Assert(err, IsNil)
	c.Assert(stored.Mode, Equals, tenancy.ModeSelfHosted)

	forced := s.auditActionsSince(c, start, "tenancy.force_self_hosted")
	c.Assert(forced, HasLen, 1)
	c.Assert(forced[0].Actor, Equals, "cluster-key")
	var data struct {
		From      string `json:"from"`
		Mode      string `json:"mode"`
		Force     bool   `json:"force"`
		OwnedApps int    `json:"owned_apps"`
		Warning   string `json:"warning"`
	}
	c.Assert(json.Unmarshal(forced[0].Data, &data), IsNil)
	c.Assert(data.From, Equals, tenancy.ModeHosted)
	c.Assert(data.Mode, Equals, tenancy.ModeSelfHosted)
	c.Assert(data.Force, Equals, true)
	c.Assert(data.OwnedApps >= 1, Equals, true)
	c.Assert(data.Warning, Matches, ".*unlimited.*")

	// Nothing else is rewritten.
	app, err := s.c.GetApp(customer.ID)
	c.Assert(err, IsNil)
	c.Assert(app.OwnerAccount, Equals, "user:force-customer")
	quota, err := s.api.tenancy.GetQuota("user:force-kept")
	c.Assert(err, IsNil)
	c.Assert(*quota.MaxApps, Equals, 2)
	rs, err := s.c.GetRuntimeSettings()
	c.Assert(err, IsNil)
	c.Assert(rs.ReserveResources, Equals, true)

	// Force on a cluster that does not need it is a plain set, not a bypass.
	code, _, _ = s.putTenancyRaw(c, `{"mode":"self_hosted","force":true}`)
	c.Assert(code, Equals, 200)
	c.Assert(s.auditActionsSince(c, start, "tenancy.force_self_hosted"), HasLen, 1)
}
