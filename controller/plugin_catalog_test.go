package main

import (
	"net/http"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/buildtoken"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// A tenant's flynn CLI builds its add-on and plugin command catalog from the
// controller. GET /apps lists only the tenant's own apps, never plugin apps,
// so addons:types was empty, addons:create redis said redis was not
// installed, and plugin commands were hidden. GET /plugin-catalog is the
// catalog a user session may use, with no secrets.
func (s *S) TestTenantPluginCatalog(c *C) {
	safe := s.createTestProvider(c, &ct.Provider{URL: "https://new42-safe.example.com", Name: "new42-safe", TenantSafe: true})
	unsafe := s.createTestProvider(c, &ct.Provider{URL: "https://new42-unsafe.example.com", Name: "new42-unsafe"})
	plugins := []*ct.App{
		s.createTestApp(c, &ct.App{Name: "new42-safe-plugin", Meta: map[string]string{
			plugin.MetaPlugin:      "true",
			plugin.MetaPluginKind:  plugin.KindResourceProvider,
			plugin.MetaPluginAddon: `{"type":"new42safe","verbs":["create","attach"],"backend":"resource","provider":"new42-safe"}`,
			plugin.MetaPluginCLI:   `{"command":"new42safe","usage":"tenant-safe store"}`,
		}}),
		s.createTestApp(c, &ct.App{Name: "new42-unsafe-plugin", Meta: map[string]string{
			plugin.MetaPlugin:      "true",
			plugin.MetaPluginKind:  plugin.KindResourceProvider,
			plugin.MetaPluginAddon: `{"type":"new42unsafe","verbs":["create","attach"],"backend":"resource","provider":"new42-unsafe"}`,
			plugin.MetaPluginCLI:   `{"command":"new42unsafe","usage":"shared store"}`,
		}}),
	}
	ua := s.createLoginUser(c, "new42-a@flynn.test", "new42-a", "pw-new42-a", false)
	s.createTestApp(c, &ct.App{Name: "new42-a", OwnerAccount: "user:" + ua.ID})
	userTok := s.signUser(c, ua.ID, nil)
	tenant, err := controller.NewClientWithToken(s.srv.URL, userTok)
	c.Assert(err, IsNil)

	// The cause: a tenant's app list has no plugin apps.
	apps, err := tenant.AppList()
	c.Assert(err, IsNil)
	for _, a := range apps {
		c.Assert(a.Plugin(), Equals, false, Commentf("tenant app list has plugin app %s", a.Name))
	}

	// self_hosted ignores tenant_safe, so the tenant sees both types.
	cat, err := plugin.LoadCatalog(tenant)
	c.Assert(err, IsNil)
	for _, typ := range []string{"new42safe", "new42unsafe"} {
		c.Assert(cat.AddonByType(typ), NotNil, Commentf("self_hosted tenant missing %s: %+v", typ, cat.Addons))
		c.Assert(cat.HasCommand(typ), Equals, true, Commentf("self_hosted tenant missing command %s", typ))
	}
	c.Assert(cat.AddonByType("new42safe").Provider, Equals, safe.Name)
	c.Assert(cat.AddonByType("new42safe").App, Equals, "new42-safe-plugin")

	// The body names plugin apps and specs only: no app ids, meta, or
	// provider URLs.
	code, body := s.scopedStatus(c, http.MethodGet, userTok, "/plugin-catalog", "")
	c.Assert(code, Equals, http.StatusOK, Commentf("body=%s", body))
	for _, leak := range []string{plugins[0].ID, plugins[1].ID, plugin.MetaPluginRecord, plugin.MetaPluginAddon, safe.URL, unsafe.URL, safe.ID} {
		c.Assert(strings.Contains(body, leak), Equals, false, Commentf("catalog leaks %q: %s", leak, body))
	}

	// hosted refuses a non-tenant_safe provider to a tenant, so the catalog
	// hides it too. A cluster admin still sees every type.
	c.Assert(s.api.tenancy.SetSettings(tenancy.ModeHosted, nil), IsNil)
	defer func() { c.Assert(s.api.tenancy.SetSettings(tenancy.ModeSelfHosted, nil), IsNil) }()
	cat, err = plugin.LoadCatalog(tenant)
	c.Assert(err, IsNil)
	c.Assert(cat.AddonByType("new42safe"), NotNil)
	c.Assert(cat.HasCommand("new42safe"), Equals, true)
	c.Assert(cat.AddonByType("new42unsafe"), IsNil, Commentf("hosted tenant sees refused type: %+v", cat.Addons))
	c.Assert(cat.HasCommand("new42unsafe"), Equals, false)

	admin, err := plugin.LoadCatalog(s.c)
	c.Assert(err, IsNil)
	c.Assert(admin.AddonByType("new42unsafe"), NotNil)
	c.Assert(admin.HasCommand("new42unsafe"), Equals, true)

	// A compile credential is not a user session.
	build, err := buildtoken.Mint(s.tokenSigner, plugins[0], time.Minute)
	c.Assert(err, IsNil)
	code, _ = s.scopedStatus(c, http.MethodGet, build, "/plugin-catalog", "")
	c.Assert(code, Equals, http.StatusForbidden)
}
