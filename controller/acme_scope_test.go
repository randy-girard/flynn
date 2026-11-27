package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/authz"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/data"
	ct "github.com/randy-girard/flynn/controller/types"
	router "github.com/randy-girard/flynn/router/types"
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

// TestACMEIssueTokenSeesUserAppRoutes is the letsencrypt plugin's view:
// with ACME_TOKEN it lists routes (paged, as the client does, and unpaged)
// and turns HTTPS on for a tenant app's route. Routes of bootstrap system
// apps stay hidden, the same as its GET /apps.
func (s *S) TestACMEIssueTokenSeesUserAppRoutes(c *C) {
	tenantApp := s.createTestApp(c, &ct.App{Name: "new22-tenant-app", OwnerAccount: "user:new22-tenant"})
	systemApp := s.createTestApp(c, &ct.App{Name: "new22-system-app", Meta: map[string]string{"flynn-system-app": "true"}})
	tenantRoute := s.createTestRoute(c, tenantApp.ID, (&router.HTTPRoute{Domain: "new22-tenant.example.com", Service: "new22-tenant-app-web"}).ToRoute())
	systemRoute := s.createTestRoute(c, systemApp.ID, (&router.HTTPRoute{Domain: "new22-system.example.com", Service: "new22-system-app-web"}).ToRoute())

	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "acme",
		UserEmail:  "acme@flynn.local",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeACMEIssue},
	})
	c.Assert(err, IsNil)

	for _, query := range []string{"", "count=1000"} {
		status, routes := s.getRoutes(c, raw, false, query)
		c.Assert(status, Equals, http.StatusOK)
		ids := map[string]bool{}
		for _, id := range routeIDs(routes) {
			ids[id] = true
		}
		c.Assert(ids["http/"+tenantRoute.ID], Equals, true, Commentf("query %q: acme:issue must see the tenant route", query))
		c.Assert(ids["http/"+systemRoute.ID], Equals, false, Commentf("query %q: acme:issue must not see a system app route", query))
	}

	client, err := controller.NewClientWithToken(s.srv.URL, raw)
	c.Assert(err, IsNil)
	routes, err := client.RouteList()
	c.Assert(err, IsNil)
	var found *router.Route
	for _, rt := range routes {
		if rt.Type == "http" && rt.ID == tenantRoute.ID {
			found = rt
		}
	}
	c.Assert(found, NotNil)

	acmeRepo := data.NewACMEConfigRepo(s.hc.db)
	prev, err := acmeRepo.Get()
	c.Assert(err, IsNil)
	enabled := *prev
	enabled.Enabled = true
	c.Assert(acmeRepo.Update(&enabled), IsNil)
	defer func() { c.Assert(acmeRepo.Update(prev), IsNil) }()

	domain := found.Domain
	found.ManagedCertificateDomain = &domain
	c.Assert(client.UpdateRoute(tenantApp.Name, "http/"+found.ID, found), IsNil)
	got, err := s.c.GetRoute(tenantApp.ID, "http/"+found.ID)
	c.Assert(err, IsNil)
	c.Assert(got.ManagedCertificateDomain, NotNil)
	c.Assert(*got.ManagedCertificateDomain, Equals, domain)

	// A system app's routes stay out of reach even by id.
	sysDomain := systemRoute.Domain
	systemRoute.ManagedCertificateDomain = &sysDomain
	c.Assert(client.UpdateRoute(systemApp.ID, "http/"+systemRoute.ID, systemRoute), NotNil)
	got, err = s.c.GetRoute(systemApp.ID, "http/"+systemRoute.ID)
	c.Assert(err, IsNil)
	c.Assert(got.ManagedCertificateDomain, IsNil)
}

// TestACMEIssueTokenOnlyTogglesHTTPS is NEW-30: the acme:issue token may
// turn HTTPS on for a tenant route with a PUT, but the PUT keeps every
// field other than the certificate ones, and the token cannot create or
// delete routes or read the cluster catalogs.
func (s *S) TestACMEIssueTokenOnlyTogglesHTTPS(c *C) {
	app := s.createTestApp(c, &ct.App{Name: "new30-tenant-app", OwnerAccount: "user:new30-tenant"})
	rt := s.createTestRoute(c, app.ID, (&router.HTTPRoute{Domain: "new30.example.com", Service: "new30-tenant-app-web"}).ToRoute())
	now := time.Now()
	raw, err := s.tokenSigner.Sign(&api.AccessToken{
		UserId:     "acme",
		UserEmail:  "acme@flynn.local",
		IssueTime:  timestamppb.New(now),
		ExpireTime: timestamppb.New(now.Add(15 * time.Minute)),
		Scopes:     []string{authz.ScopeACMEIssue},
	})
	c.Assert(err, IsNil)

	acmeRepo := data.NewACMEConfigRepo(s.hc.db)
	prev, err := acmeRepo.Get()
	c.Assert(err, IsNil)
	enabled := *prev
	enabled.Enabled = true
	c.Assert(acmeRepo.Update(&enabled), IsNil)
	defer func() { c.Assert(acmeRepo.Update(prev), IsNil) }()

	client, err := controller.NewClientWithToken(s.srv.URL, raw)
	c.Assert(err, IsNil)
	got, err := s.c.GetRoute(app.ID, "http/"+rt.ID)
	c.Assert(err, IsNil)
	domain := got.Domain
	got.ManagedCertificateDomain = &domain
	got.Service = "new30-tenant-app-other"
	got.Sticky = true
	c.Assert(client.UpdateRoute(app.Name, "http/"+rt.ID, got), IsNil)
	after, err := s.c.GetRoute(app.ID, "http/"+rt.ID)
	c.Assert(err, IsNil)
	c.Assert(after.ManagedCertificateDomain, NotNil)
	c.Assert(*after.ManagedCertificateDomain, Equals, domain)
	c.Assert(after.Service, Equals, "new30-tenant-app-web")
	c.Assert(after.Sticky, Equals, false)

	// Turning HTTPS off is the other change it may make.
	after.ManagedCertificateDomain = nil
	c.Assert(client.UpdateRoute(app.Name, "http/"+rt.ID, after), IsNil)
	off, err := s.c.GetRoute(app.ID, "http/"+rt.ID)
	c.Assert(err, IsNil)
	c.Assert(off.ManagedCertificateDomain, IsNil)

	for _, req := range [][3]string{
		{http.MethodPost, "/apps/" + app.ID + "/routes", `{"type":"http","domain":"new30-extra.example.com","service":"attacker-web"}`},
		{http.MethodDelete, "/apps/" + app.ID + "/routes/http/" + rt.ID, ""},
		{http.MethodGet, "/apps/" + app.ID + "/routes/http/" + rt.ID, ""},
		{http.MethodGet, "/hosts", ""},
		{http.MethodGet, "/providers", ""},
	} {
		code, _ := s.scopedStatus(c, req[0], raw, req[1], req[2])
		c.Assert(code, Equals, http.StatusForbidden, Commentf("%s %s", req[0], req[1]))
	}
	_, err = s.c.GetRoute(app.ID, "http/"+rt.ID)
	c.Assert(err, IsNil)
}

// TestTenantReadsOnlyACMEEnabled is NEW-29: `flynn letsencrypt:enable` and
// `:status` read GET /acme/config with the caller's own token. A tenant
// user may read it and sees only whether ACME is on; the contact email,
// directory and account state stay with operators.
func (s *S) TestTenantReadsOnlyACMEEnabled(c *C) {
	acmeRepo := data.NewACMEConfigRepo(s.hc.db)
	prev, err := acmeRepo.Get()
	c.Assert(err, IsNil)
	cfg := *prev
	cfg.Enabled = true
	cfg.ContactEmail = "ops-new29@example.com"
	cfg.TermsOfServiceAgreed = true
	c.Assert(acmeRepo.Update(&cfg), IsNil)
	defer func() { c.Assert(acmeRepo.Update(prev), IsNil) }()

	tenant := s.signUser(c, "new29-tenant", nil)
	code, body := s.scopedStatus(c, http.MethodGet, tenant, "/acme/config", "")
	c.Assert(code, Equals, http.StatusOK)
	var got map[string]interface{}
	c.Assert(json.Unmarshal([]byte(body), &got), IsNil)
	c.Assert(got["enabled"], Equals, true)
	c.Assert(len(got), Equals, 1, Commentf("tenant view %s", body))
	c.Assert(strings.Contains(body, "ops-new29"), Equals, false)

	code, _ = s.scopedStatus(c, http.MethodPut, tenant, "/acme/config", `{"enabled":false}`)
	c.Assert(code, Equals, http.StatusForbidden)
}
