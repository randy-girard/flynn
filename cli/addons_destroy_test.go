package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	docopt "github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// addonFakeClient is an in-memory controller for addons:destroy and
// addons:detach: apps (by name and id), their current release env, the
// resources and an ordered log of the writes.
type addonFakeClient struct {
	controller.Client
	provider  *ct.Provider
	apps      map[string]*ct.App // by name
	releases  map[string]*ct.Release
	resources []*ct.Resource
	deleteErr error
	calls     []string
	nextID    int
}

func newAddonFakeClient() *addonFakeClient {
	return &addonFakeClient{
		provider: &ct.Provider{ID: "prov-mysql", Name: "mysql"},
		apps:     map[string]*ct.App{},
		releases: map[string]*ct.Release{},
	}
}

func (c *addonFakeClient) addApp(name string, env map[string]string) *ct.App {
	app := &ct.App{ID: name + "-id", Name: name, Meta: map[string]string{}}
	c.apps[name] = app
	if env != nil {
		c.releases[app.ID] = &ct.Release{ID: name + "-r0", Env: env}
		app.ReleaseID = name + "-r0"
	}
	return app
}

func (c *addonFakeClient) app(ref string) *ct.App {
	for _, a := range c.apps {
		if a.ID == ref || a.Name == ref {
			return a
		}
	}
	return nil
}

func (c *addonFakeClient) GetApp(ref string) (*ct.App, error) {
	if a := c.app(ref); a != nil {
		cp := *a
		cp.Meta = map[string]string{}
		for k, v := range a.Meta {
			cp.Meta[k] = v
		}
		return &cp, nil
	}
	return nil, controller.ErrNotFound
}

func (c *addonFakeClient) UpdateAppMeta(app *ct.App) error {
	a := c.app(app.ID)
	if a == nil {
		return controller.ErrNotFound
	}
	c.calls = append(c.calls, "meta:"+a.Name)
	a.Meta = app.Meta
	return nil
}

func (c *addonFakeClient) GetAppRelease(ref string) (*ct.Release, error) {
	a := c.app(ref)
	if a == nil {
		return nil, controller.ErrNotFound
	}
	rel, ok := c.releases[a.ID]
	if !ok {
		return nil, controller.ErrNotFound
	}
	cp := *rel
	cp.Env = map[string]string{}
	for k, v := range rel.Env {
		cp.Env[k] = v
	}
	return &cp, nil
}

func (c *addonFakeClient) DeploymentList(string) ([]*ct.Deployment, error) { return nil, nil }

func (c *addonFakeClient) CreateRelease(appID string, rel *ct.Release) error {
	a := c.app(appID)
	if a == nil {
		return controller.ErrNotFound
	}
	c.nextID++
	rel.ID = fmt.Sprintf("%s-r%d", a.Name, c.nextID)
	c.calls = append(c.calls, "release:"+a.Name)
	cp := *rel
	c.releases[a.ID+"/"+rel.ID] = &cp
	return nil
}

func (c *addonFakeClient) SetAppRelease(appID, releaseID string) error {
	a := c.app(appID)
	if a == nil {
		return controller.ErrNotFound
	}
	c.releases[a.ID] = c.releases[a.ID+"/"+releaseID]
	a.ReleaseID = releaseID
	return nil
}

func (c *addonFakeClient) GetProvider(id string) (*ct.Provider, error) {
	if id == c.provider.ID || id == c.provider.Name {
		return c.provider, nil
	}
	return nil, controller.ErrNotFound
}

func (c *addonFakeClient) AppResourceList(ref string) ([]*ct.Resource, error) {
	a := c.app(ref)
	if a == nil {
		return nil, controller.ErrNotFound
	}
	var out []*ct.Resource
	for _, r := range c.resources {
		for _, id := range r.Apps {
			if id == a.ID {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

func (c *addonFakeClient) ResourceListAll() ([]*ct.Resource, error) { return c.resources, nil }

func (c *addonFakeClient) ResourceList(string) ([]*ct.Resource, error) { return c.resources, nil }

func (c *addonFakeClient) ListPluginAddons(string) ([]*ct.PluginAddon, error) { return nil, nil }

func (c *addonFakeClient) DeleteResource(provider, id string) (*ct.Resource, error) {
	c.calls = append(c.calls, "delete:"+id)
	if c.deleteErr != nil {
		return nil, c.deleteErr
	}
	for i, r := range c.resources {
		if r.ID == id {
			c.resources = append(c.resources[:i], c.resources[i+1:]...)
			return r, nil
		}
	}
	return nil, controller.ErrNotFound
}

func (c *addonFakeClient) DeleteResourceApp(provider, id, appID string) (*ct.Resource, error) {
	c.calls = append(c.calls, "unbind:"+id+":"+appID)
	for _, r := range c.resources {
		if r.ID != id {
			continue
		}
		var apps []string
		for _, a := range r.Apps {
			if a != appID {
				apps = append(apps, a)
			}
		}
		r.Apps = apps
		return r, nil
	}
	return nil, controller.ErrNotFound
}

func (c *addonFakeClient) env(name string) map[string]string {
	return c.releases[c.apps[name].ID].Env
}

func (c *addonFakeClient) wrote(prefix string) bool {
	for _, call := range c.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func (c *addonFakeClient) index(call string) int {
	for i, got := range c.calls {
		if got == call {
			return i
		}
	}
	return -1
}

func useAddonTestCatalog(t *testing.T) {
	t.Helper()
	cat := &plugin.Catalog{Addons: []plugin.AddonSpec{{
		Type:     "mysql",
		Provider: "mysql",
		Backend:  plugin.BackendResource,
		Verbs:    []string{"create", "attach", "detach", "destroy"},
		Env:      &plugin.AddonEnv{Prefix: "FLYNN_MYSQL_", Conventional: "DATABASE_URL"},
		Destroy:  &plugin.AddonDestroy{FollowerEnv: []string{"FLYNN_MYSQL_LEADER"}},
	}}}
	prev := fetchPluginCatalog
	fetchPluginCatalog = func() (*plugin.Catalog, error) { return cat, nil }
	t.Cleanup(func() { fetchPluginCatalog = prev })
}

func useApp(t *testing.T, name string) {
	t.Helper()
	prev := flagApp
	flagApp = name
	t.Cleanup(func() { flagApp = prev })
}

func destroyArgs(addon string) *docopt.Args {
	return &docopt.Args{
		String: map[string]string{"<provider>": addon, "<addon>": ""},
		Bool:   map[string]bool{"--yes": true},
	}
}

const (
	mysqlPrimaryURL  = "mysql://a:p@leader.mysql-prairie-90441.discoverd:3306/db"
	mysqlFollowerURL = "mysql://b:q@leader.mysql-pond-58191.discoverd:3306/db"
)

func mysqlPrimary(apps ...string) *ct.Resource {
	return &ct.Resource{
		ID:         "res-primary",
		ProviderID: "prov-mysql",
		OwnerApp:   apps[0],
		Apps:       apps,
		Env: map[string]string{
			"FLYNN_MYSQL":             "mysql-prairie-90441",
			"FLYNN_MYSQL_GREEN_URL":   mysqlPrimaryURL,
			"DATABASE_URL":            mysqlPrimaryURL,
			"FLYNN_MYSQL_ROLE":        "primary",
			"FLYNN_MYSQL_TLS_ENABLED": "true",
		},
	}
}

func mysqlFollower(apps ...string) *ct.Resource {
	return &ct.Resource{
		ID:         "res-follower",
		ProviderID: "prov-mysql",
		OwnerApp:   apps[0],
		Apps:       apps,
		Env: map[string]string{
			"FLYNN_MYSQL":             "mysql-pond-58191",
			"FLYNN_MYSQL_GRAY_URL":    mysqlFollowerURL,
			"FLYNN_MYSQL_ROLE":        "follower",
			"FLYNN_MYSQL_LEADER":      "mysql-prairie-90441",
			"FLYNN_MYSQL_TLS_ENABLED": "true",
		},
	}
}

func primaryAppEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":            mysqlPrimaryURL,
		"FLYNN_MYSQL_GREEN_URL":   mysqlPrimaryURL,
		"FLYNN_MYSQL_ROLE":        "primary",
		"FLYNN_MYSQL_TLS_ENABLED": "true",
		"KEEP":                    "yes",
	}
}

// NEW-36(a): a provider refusal (followers, datastore guard) must leave the
// tenant's env and attachment record as they were.
func TestAddonsDestroyRefusedByProviderKeepsEnv(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	a := c.addApp("shop", primaryAppEnv())
	a.Meta[metaAddonAttachments] = `{"GREEN":{"addon":"mysql-prairie-90441","resource_id":"res-primary","env_key":"FLYNN_MYSQL_GREEN_URL"}}`
	c.resources = []*ct.Resource{mysqlPrimary(a.ID)}
	c.deleteErr = errors.New("validation_error: mysql cannot remove a resource while it still has followers: mysql-pond-58191")
	useApp(t, "shop")

	err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c)
	if err == nil || !strings.Contains(err.Error(), "followers") {
		t.Fatalf("want the provider's refusal, got %v", err)
	}
	if c.wrote("release:") || c.wrote("meta:") {
		t.Fatalf("a refused destroy must not touch the app: %v", c.calls)
	}
	if c.env("shop")["FLYNN_MYSQL_GREEN_URL"] != mysqlPrimaryURL {
		t.Fatalf("env changed: %v", c.env("shop"))
	}
}

// NEW-36(a): the env and the attachment record go only after the provider
// accepted the delete.
func TestAddonsDestroyUnsetsEnvAfterProviderAccepts(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	a := c.addApp("shop", primaryAppEnv())
	a.Meta[metaAddonAttachments] = `{"GREEN":{"addon":"mysql-prairie-90441","resource_id":"res-primary","env_key":"FLYNN_MYSQL_GREEN_URL"}}`
	c.resources = []*ct.Resource{mysqlPrimary(a.ID)}
	useApp(t, "shop")

	if err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c); err != nil {
		t.Fatal(err)
	}
	del, rel := c.index("delete:res-primary"), c.index("release:shop")
	if del < 0 || rel < 0 || del > rel {
		t.Fatalf("delete must come before the env change: %v", c.calls)
	}
	env := c.env("shop")
	for _, k := range []string{"DATABASE_URL", "FLYNN_MYSQL_GREEN_URL", "FLYNN_MYSQL_ROLE", "FLYNN_MYSQL_TLS_ENABLED"} {
		if _, ok := env[k]; ok {
			t.Fatalf("%s must be unset: %v", k, env)
		}
	}
	if env["KEEP"] != "yes" {
		t.Fatalf("unrelated env lost: %v", env)
	}
	if strings.Contains(c.apps["shop"].Meta[metaAddonAttachments], "res-primary") {
		t.Fatalf("attachment record kept: %v", c.apps["shop"].Meta)
	}
}

// NEW-47: destroying on the owning app also removes the attachment env from
// every other attached app.
func TestAddonsDestroyUnsetsEnvOnAttachedApps(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	owner := c.addApp("shop", primaryAppEnv())
	other := c.addApp("reports", map[string]string{"FLYNN_MYSQL_GREEN_URL": mysqlPrimaryURL, "FLYNN_MYSQL_ROLE": "primary", "OWN": "1"})
	other.Meta[metaAddonAttachments] = `{"GREEN":{"addon":"mysql-prairie-90441","resource_id":"res-primary","env_key":"FLYNN_MYSQL_GREEN_URL"}}`
	res := mysqlPrimary(owner.ID, other.ID)
	res.OwnerApp = owner.ID
	c.resources = []*ct.Resource{res}
	useApp(t, "shop")

	if err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c); err != nil {
		t.Fatal(err)
	}
	env := c.env("reports")
	if _, ok := env["FLYNN_MYSQL_GREEN_URL"]; ok {
		t.Fatalf("attached app keeps the URL: %v", env)
	}
	if _, ok := env["FLYNN_MYSQL_ROLE"]; ok {
		t.Fatalf("attached app keeps the role: %v", env)
	}
	if env["OWN"] != "1" {
		t.Fatalf("attached app lost its own env: %v", env)
	}
	if strings.Contains(c.apps["reports"].Meta[metaAddonAttachments], "res-primary") {
		t.Fatalf("attached app keeps the attachment record: %v", c.apps["reports"].Meta)
	}
}

// DATA-72: destroying a follower keeps the shared keys the still-attached
// primary provides and restores the primary's role.
func TestAddonsDestroyFollowerKeepsPrimarySharedEnv(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	env := primaryAppEnv()
	env["FLYNN_MYSQL_GRAY_URL"] = mysqlFollowerURL
	env["FLYNN_MYSQL_ROLE"] = "follower"
	env["FLYNN_MYSQL_LEADER"] = "mysql-prairie-90441"
	a := c.addApp("shop", env)
	c.resources = []*ct.Resource{mysqlPrimary(a.ID), mysqlFollower(a.ID)}
	useApp(t, "shop")

	if err := runAddonsDestroy(destroyArgs("mysql-pond-58191"), c); err != nil {
		t.Fatal(err)
	}
	got := c.env("shop")
	if _, ok := got["FLYNN_MYSQL_GRAY_URL"]; ok {
		t.Fatalf("follower URL kept: %v", got)
	}
	if got["FLYNN_MYSQL_TLS_ENABLED"] != "true" || got["FLYNN_MYSQL_GREEN_URL"] != mysqlPrimaryURL || got["DATABASE_URL"] != mysqlPrimaryURL {
		t.Fatalf("primary's env lost: %v", got)
	}
	if got["FLYNN_MYSQL_ROLE"] != "primary" {
		t.Fatalf("role must come back to the primary's: %v", got)
	}
	if _, ok := got["FLYNN_MYSQL_LEADER"]; ok {
		t.Fatalf("leader kept with no follower left: %v", got)
	}
}

// DATA-68: an app with no release (its resource came from a path that set no
// env) can still destroy its own add-on.
func TestAddonsDestroyAppWithNoRelease(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	a := c.addApp("bare", nil)
	c.resources = []*ct.Resource{mysqlPrimary(a.ID)}
	useApp(t, "bare")

	if err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c); err != nil {
		t.Fatalf("destroy on an app with no release: %v", err)
	}
	if c.index("delete:res-primary") < 0 {
		t.Fatalf("resource not deleted: %v", c.calls)
	}
}

// NEW-65: two resources with one name are an ambiguity, not "not found".
func TestAddonsDestroyAmbiguousNameNamesBothIDs(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	a := c.addApp("shop", primaryAppEnv())
	one := mysqlPrimary(a.ID)
	two := mysqlPrimary(a.ID)
	two.ID = "res-old-row"
	c.resources = []*ct.Resource{one, two}
	useApp(t, "shop")

	err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c)
	if err == nil || strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), "res-primary") || !strings.Contains(err.Error(), "res-old-row") {
		t.Fatalf("want an ambiguity error naming both ids, got %v", err)
	}
	if c.wrote("delete:") {
		t.Fatalf("nothing may be deleted: %v", c.calls)
	}
}

// NEW-27: a resource created with --unattached can be destroyed without -a.
func TestAddonsDestroyUnattachedWithoutApp(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	res := mysqlPrimary("x")
	res.Apps = nil
	res.OwnerApp = ""
	res.OwnerAccount = "user:1"
	c.resources = []*ct.Resource{res}
	prevApp, prevRemote := flagApp, flagRemote
	flagApp, flagRemote = "", ""
	t.Cleanup(func() { flagApp, flagRemote = prevApp, prevRemote })
	t.Setenv("FLYNN_APP", "")
	prevNoApp := addonsNoApp
	addonsNoApp = func() bool { return true }
	t.Cleanup(func() { addonsNoApp = prevNoApp })

	if err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c); err != nil {
		t.Fatalf("destroy of an unattached resource with no app: %v", err)
	}
	if c.index("delete:res-primary") < 0 {
		t.Fatalf("resource not deleted: %v", c.calls)
	}
}

// NEW-27: with no app, an attached resource still needs its owning app.
func TestAddonsDestroyWithoutAppRefusesAttachedResource(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	a := c.addApp("shop", primaryAppEnv())
	c.resources = []*ct.Resource{mysqlPrimary(a.ID)}
	prevNoApp := addonsNoApp
	addonsNoApp = func() bool { return true }
	t.Cleanup(func() { addonsNoApp = prevNoApp })

	err := runAddonsDestroy(destroyArgs("mysql-prairie-90441"), c)
	if err == nil || !strings.Contains(err.Error(), "-a shop") {
		t.Fatalf("want a hint to use the owning app, got %v", err)
	}
	if c.wrote("delete:") {
		t.Fatalf("nothing may be deleted: %v", c.calls)
	}
}

func detachArgs(name string) *docopt.Args {
	return &docopt.Args{String: map[string]string{"<attachment>": name}}
}

// NEW-47: addons:detach accepts the add-on name (and id) of a resource this
// app is attached to, like addons:attach does.
func TestAddonsDetachByAddonName(t *testing.T) {
	for _, ref := range []string{"mysql-prairie-90441", "res-primary"} {
		t.Run(ref, func(t *testing.T) {
			useAddonTestCatalog(t)
			c := newAddonFakeClient()
			owner := c.addApp("shop", primaryAppEnv())
			other := c.addApp("reports", map[string]string{"FLYNN_MYSQL_GREEN_URL": mysqlPrimaryURL, "FLYNN_MYSQL_ROLE": "primary", "OWN": "1"})
			other.Meta[metaAddonAttachments] = `{"GREEN":{"addon":"mysql-prairie-90441","resource_id":"res-primary","env_key":"FLYNN_MYSQL_GREEN_URL"}}`
			c.resources = []*ct.Resource{mysqlPrimary(owner.ID, other.ID)}
			c.resources[0].OwnerApp = owner.ID
			useApp(t, "reports")

			if err := runAddonsDetach(detachArgs(ref), c); err != nil {
				t.Fatal(err)
			}
			if c.index("unbind:res-primary:reports-id") < 0 {
				t.Fatalf("not unbound: %v", c.calls)
			}
			env := c.env("reports")
			if _, ok := env["FLYNN_MYSQL_GREEN_URL"]; ok {
				t.Fatalf("URL kept: %v", env)
			}
			if env["OWN"] != "1" {
				t.Fatalf("own env lost: %v", env)
			}
			if c.wrote("delete:") {
				t.Fatalf("detach must not delete: %v", c.calls)
			}
		})
	}
}

// The owning app cannot detach its own add-on by name; it destroys it.
func TestAddonsDetachByAddonNameOwnerRefused(t *testing.T) {
	useAddonTestCatalog(t)
	c := newAddonFakeClient()
	owner := c.addApp("shop", primaryAppEnv())
	c.resources = []*ct.Resource{mysqlPrimary(owner.ID)}
	useApp(t, "shop")

	err := runAddonsDetach(detachArgs("mysql-prairie-90441"), c)
	if err == nil || !strings.Contains(err.Error(), "addons:destroy") {
		t.Fatalf("want a hint to use addons:destroy, got %v", err)
	}
	if c.wrote("unbind:") || c.wrote("release:") {
		t.Fatalf("nothing may change: %v", c.calls)
	}
}
