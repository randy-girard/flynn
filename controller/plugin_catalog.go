package main

import (
	"net/http"

	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/plugin"
	"golang.org/x/net/context"
)

// pluginCatalogPage is the page size for the plugin app walk. A cluster has
// a few dozen plugins at most.
const pluginCatalogPage = 1000

// GetPluginCatalog is GET /plugin-catalog: the plugin commands and add-on
// types the caller may use, the catalog the flynn CLI used to build from
// GET /apps?all=true. A tenant's GET /apps lists only its own apps, so that
// catalog was empty for every non-admin. The body is the CLI and add-on
// specs from plugin manifests and the plugin app names: no app ids, meta,
// env, or provider URLs. In hosted mode a non-operator does not see a
// provider without tenant_safe, which ProvisionResource refuses.
func (c *controllerAPI) GetPluginCatalog(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	apps, err := c.pluginApps()
	if err != nil {
		respondWithError(w, err)
		return
	}
	raw, err := c.providerRepo.List()
	if err != nil {
		respondWithError(w, err)
		return
	}
	providers, _ := raw.([]*ct.Provider)
	restricted, err := c.hostedTenantRestricted(ctx)
	if err != nil {
		respondWithError(w, err)
		return
	}
	var allow func(*ct.Provider) bool
	if restricted {
		allow = func(p *ct.Provider) bool { return p.TenantSafe }
	}
	cat := plugin.UserCatalog(apps, providers, allow)
	if cat.Commands == nil {
		cat.Commands = []plugin.CLI{}
	}
	httphelper.JSON(w, 200, cat)
}

// pluginApps is every app with flynn-plugin=true.
func (c *controllerAPI) pluginApps() ([]*ct.App, error) {
	filter := []ct.LabelFilter{{&ct.LabelFilterExpression{
		Op:     ct.LabelFilterExpressionOpIn,
		Key:    plugin.MetaPlugin,
		Values: []string{"true"},
	}}}
	var out []*ct.App
	seen := map[string]struct{}{}
	token := data.PageToken{Size: pluginCatalogPage}
	for {
		page, next, err := c.appRepo.ListPage(data.ListAppOptions{PageToken: token, LabelFilters: filter})
		if err != nil {
			return nil, err
		}
		added := 0
		for _, app := range page {
			if _, ok := seen[app.ID]; ok {
				continue
			}
			seen[app.ID] = struct{}{}
			out = append(out, app)
			added++
		}
		// app_list_page's cursor is created_at <=, so a page can repeat
		// rows; stop when one adds nothing.
		if next == nil || added == 0 {
			return out, nil
		}
		token = *next
	}
}

// hostedTenantRestricted is true when the caller may provision only
// tenant_safe providers: hosted mode and not an operator.
func (c *controllerAPI) hostedTenantRestricted(ctx context.Context) (bool, error) {
	if c == nil || c.tenancy == nil {
		return false, nil
	}
	tok := authz.TokenFromContext(ctx)
	if tok == nil || tok.ClusterKey || tok.HasClusterAdmin() {
		return false, nil
	}
	settings, err := c.tenancy.GetSettings()
	if err != nil {
		return false, err
	}
	return settings.Mode == tenancy.ModeHosted, nil
}
