package plugin

import (
	"strings"

	"github.com/randy-girard/flynn/pkg/dbruntime"
)

type dbRuntimeAPI interface {
	ListDBRuntimes() (*dbruntime.Catalog, error)
	ReplaceDBRuntimes(catalog *dbruntime.Catalog) error
}

// pluginDBRuntimeEngine is the catalog engine for a datastore plugin, or "".
func pluginDBRuntimeEngine(name, provider string, datastore bool) string {
	if e, ok := dbruntime.ProviderEngine(provider); ok {
		return e
	}
	if e, ok := dbruntime.ProviderEngine(name); ok {
		return e
	}
	if datastore {
		trim := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), "-plugin")
		if e, ok := dbruntime.ProviderEngine(trim); ok {
			return e
		}
	}
	return ""
}

func (in *Installer) runtimeAPI() dbRuntimeAPI {
	if in == nil {
		return nil
	}
	if in.dbRuntimeAPI != nil {
		return in.dbRuntimeAPI
	}
	if in.Client == nil {
		return nil
	}
	api, _ := in.Client.(dbRuntimeAPI)
	return api
}

// loadPluginDBRuntimeCatalog prefers the controller catalog so a plugin
// install cannot replace other engines with a stale host file. An empty
// controller catalog falls back to the host cache (seed after update).
func loadPluginDBRuntimeCatalog(api dbRuntimeAPI, path string) dbruntime.Catalog {
	var live *dbruntime.Catalog
	if api != nil {
		if got, err := api.ListDBRuntimes(); err == nil {
			live = got
		}
	}
	file, err := dbruntime.Load(path)
	if err != nil {
		file = dbruntime.EmptyCatalog()
	}
	cat, _ := dbruntime.MergeLiveAndFile(live, file)
	return cat
}

func (in *Installer) syncPluginDBRuntimes(engine string, add bool) {
	if in == nil || engine == "" {
		return
	}
	path := dbruntime.Path()
	cat := loadPluginDBRuntimeCatalog(in.runtimeAPI(), path)
	if add {
		if err := cat.EnsureEngine(engine); err != nil {
			in.logf("warning: ensure %s database runtimes: %s", engine, err)
			return
		}
		in.logf("published %s database runtimes (small, medium, large)", engine)
	} else {
		if err := cat.RemoveEngine(engine); err != nil {
			in.logf("warning: remove %s database runtimes: %s", engine, err)
			return
		}
		in.logf("removed %s database runtimes", engine)
	}
	if err := dbruntime.Save(path, cat); err != nil {
		in.logf("warning: save database runtimes: %s", err)
		return
	}
	pub := in.runtimeAPI()
	if pub == nil {
		return
	}
	if err := pub.ReplaceDBRuntimes(&cat); err != nil {
		in.logf("warning: publish database runtimes: %s", err)
	}
}

func (in *Installer) ensureManifestDBRuntimes(m *Manifest) {
	if m == nil {
		return
	}
	provider := ""
	if m.Provider != nil {
		provider = m.Provider.Name
	}
	in.syncPluginDBRuntimes(pluginDBRuntimeEngine(m.Name, provider, m.Record().Datastore), true)
}

func (in *Installer) removeInstalledDBRuntimes(rec Installed) {
	in.syncPluginDBRuntimes(pluginDBRuntimeEngine(rec.Name, rec.Provider, rec.Datastore), false)
}
