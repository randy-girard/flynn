package plugin

import (
	"fmt"
	"os"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

// uninstallAPI is the controller subset used to tear down an installed plugin.
// Tests replace Installer.UninstallClient.
type uninstallAPI interface {
	AppList() ([]*ct.App, error)
	DeleteApp(appID string) (*ct.AppDeletion, error)
	ResourceList(providerID string) ([]*ct.Resource, error)
}

// UninstallOptions is flynn-host plugin:uninstall.
type UninstallOptions struct {
	Name        string
	Force       bool
	Cwd         string
	GitHubOrg   string
	PluginsFile string
	CredsFile   string
}

func (in *Installer) uninstallAPI() uninstallAPI {
	if in != nil && in.UninstallClient != nil {
		return in.UninstallClient
	}
	if in != nil && in.Client != nil {
		return in.Client
	}
	return nil
}

// Uninstall removes an installed plugin app, its host webhooks, and optional
// uninstall hook. Flynn does not special-case plugin names. Resource-provider
// plugins with provisioned resources still in use refuse unless Force is set.
// DeleteApp already drops the plugin's HTTP/TCP routes and exclusive resources.
func (in *Installer) Uninstall(opts UninstallOptions) error {
	api := in.uninstallAPI()
	if api == nil {
		return fmt.Errorf("missing controller client")
	}
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		return fmt.Errorf("plugin name is required")
	}
	apps, err := api.AppList()
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	app, err := LookupPluginApp(apps, name)
	if err != nil {
		return err
	}
	rec := RecordFromApp(app)
	in.logf("uninstalling plugin %s (kind %s)", rec.Name, rec.Kind)

	if err := in.ensureProviderUnused(rec, app.ID, opts.Force); err != nil {
		return err
	}
	if err := in.runUninstallHook(rec, opts); err != nil {
		return err
	}
	if err := in.removePluginWebhooks(rec.Name); err != nil {
		return err
	}

	in.logf("deleting app %s", app.Name)
	if _, err := api.DeleteApp(app.ID); err != nil {
		return fmt.Errorf("delete app %s: %w", app.Name, err)
	}
	in.persistInventory()
	in.logf("plugin %s uninstalled", rec.Name)
	return nil
}

func (in *Installer) ensureProviderUnused(rec Installed, pluginAppID string, force bool) error {
	provider := strings.TrimSpace(rec.Provider)
	if provider == "" && rec.Kind != KindResourceProvider {
		return nil
	}
	if provider == "" {
		provider = rec.Name
	}
	api := in.uninstallAPI()
	if api == nil {
		return nil
	}
	resources, err := api.ResourceList(provider)
	if err != nil {
		in.logf("warning: could not list %s resources: %s", provider, err)
		return nil
	}
	n := 0
	for _, r := range resources {
		if r == nil {
			continue
		}
		for _, appID := range r.Apps {
			if appID != "" && appID != pluginAppID {
				n++
				break
			}
		}
	}
	if n == 0 {
		return nil
	}
	if !force {
		return fmt.Errorf("%s still has %d provisioned resource(s); deprovision them or pass --force", provider, n)
	}
	in.logf("warning: uninstalling %s with %d provisioned resource(s) still in use (--force)", provider, n)
	return nil
}

func (in *Installer) runUninstallHook(rec Installed, opts UninstallOptions) error {
	source := strings.TrimSpace(rec.Source)
	if source == "" {
		source = rec.Name
	}
	resolved, err := Resolve(InstallOptions{
		Source:      source,
		Ref:         rec.Ref,
		Cwd:         opts.Cwd,
		GitHubOrg:   opts.GitHubOrg,
		PluginsFile: opts.PluginsFile,
		CredsFile:   opts.CredsFile,
	})
	if err != nil {
		in.logf("skipping uninstall hook (could not resolve plugin source: %s)", err)
		return nil
	}
	root := resolved.Dir
	cleanup := func() {}
	if resolved.GitHub != nil {
		dir, err := in.fetchGitHub(resolved.GitHub, opts.CredsFile)
		if err != nil {
			in.logf("skipping uninstall hook (could not fetch GitHub source: %s)", err)
			return nil
		}
		cleanup = func() { os.RemoveAll(dir) }
		root = dir
	}
	defer cleanup()
	m, err := LoadManifest(root)
	if err != nil {
		in.logf("skipping uninstall hook: %s", err)
		return nil
	}
	rel := m.uninstallHook()
	if rel == "" {
		return nil
	}
	cluster := map[string]string{}
	if in.Client != nil {
		if env, err := ClusterEnv(in.Client); err == nil {
			cluster = env
		}
	}
	return in.runHook(root, m, rel, cluster)
}

func (in *Installer) removePluginWebhooks(pluginName string) error {
	hosts, err := in.webhookHosts()
	if err != nil {
		in.logf("warning: could not list hosts to remove plugin webhooks: %s", err)
		return nil
	}
	prefix := pluginWebhookIDPrefix(pluginName)
	for _, h := range hosts {
		listed, err := h.ListWebhooks()
		if err != nil {
			return fmt.Errorf("list webhooks on %s: %w", h.ID(), err)
		}
		for _, wh := range listed {
			if wh == nil || !strings.HasPrefix(wh.ID, prefix) {
				continue
			}
			if err := h.RemoveWebhook(wh.ID); err != nil {
				return fmt.Errorf("remove webhook %s on %s: %w", wh.ID, h.ID(), err)
			}
			in.logf("removed webhook %s from %s", wh.ID, h.ID())
		}
	}
	return nil
}
