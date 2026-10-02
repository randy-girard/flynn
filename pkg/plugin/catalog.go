package plugin

import (
	"encoding/json"

	ct "github.com/randy-girard/flynn/controller/types"
)

// catalogSource is the controller surface LoadCatalog needs. The full
// controller.Client satisfies it; tests can stub just these two calls.
type catalogSource interface {
	AppList() ([]*ct.App, error)
	ProviderList() ([]*ct.Provider, error)
}

// Catalog is the cluster's installed plugin CLI commands published on the
// user flynn CLI. Resource-provider and scheduler plugins are included;
// kind: app system plugins are not unless cli.user is true.
type Catalog struct {
	Commands []CLI `json:"commands"`
}

func (c *Catalog) HasCommand(name string) bool {
	return c.Lookup(name) != nil
}

func (c *Catalog) Lookup(name string) *CLI {
	if c == nil {
		return nil
	}
	for i := range c.Commands {
		if c.Commands[i].Command == name {
			return &c.Commands[i]
		}
	}
	return nil
}

func (c *Catalog) HasProvider(name string) bool {
	if c == nil {
		return false
	}
	for _, cmd := range c.Commands {
		if cmd.Command == name {
			return true
		}
	}
	return false
}

// LoadCatalog lists plugin apps and providers on the cluster for the laptop
// flynn CLI (UserVisible plugins).
func LoadCatalog(client catalogSource) (*Catalog, error) {
	return loadCatalog(client, false)
}

// LoadHostCatalog lists every plugin CLI, including kind: app system plugins
// that are not on the user flynn CLI. flynn-host uses this for cluster-scoped
// operator commands.
func LoadHostCatalog(client catalogSource) (*Catalog, error) {
	return loadCatalog(client, true)
}

func loadCatalog(client catalogSource, allPlugins bool) (*Catalog, error) {
	apps, err := client.AppList()
	if err != nil {
		return nil, err
	}
	providers, err := client.ProviderList()
	if err != nil {
		return catalogFromOpts(apps, nil, allPlugins), nil
	}
	return catalogFromOpts(apps, providers, allPlugins), nil
}

func catalogFrom(apps []*ct.App, providers []*ct.Provider) *Catalog {
	return catalogFromOpts(apps, providers, false)
}

func catalogFromOpts(apps []*ct.App, providers []*ct.Provider, allPlugins bool) *Catalog {
	cat := &Catalog{}
	seen := map[string]struct{}{}

	for _, app := range apps {
		if app == nil || !app.Plugin() {
			continue
		}
		raw := app.Meta[MetaPluginCLI]
		if raw == "" {
			continue
		}
		var cli CLI
		if err := json.Unmarshal([]byte(raw), &cli); err != nil || cli.Command == "" {
			continue
		}
		kind := app.Meta[MetaPluginKind]
		if kind == "" {
			kind = RecordFromApp(app).Kind
		}
		if !allPlugins && !cli.UserVisible(kind) {
			continue
		}
		if _, ok := seen[cli.Command]; ok {
			continue
		}
		seen[cli.Command] = struct{}{}
		cli.App = app.Name
		cat.Commands = append(cat.Commands, cli)
	}

	if allPlugins {
		return cat
	}

	for _, p := range providers {
		if p == nil || p.Name == "" {
			continue
		}
		if _, ok := seen[p.Name]; ok {
			continue
		}
		// Providers without a cli block still unlock a same-named flynn command
		// for compiled-in handlers. Fully extracted plugins must stamp a
		// runnable CLI spec (doc + actions) at install.
		seen[p.Name] = struct{}{}
		cat.Commands = append(cat.Commands, CLI{Command: p.Name})
	}
	return cat
}

// CorePluginCommands are compiled-in flynn handlers for plugins that are not
// yet fully extracted. Every first-party appliance CLI now lives on the plugin
// and is fetched from the cluster catalog after flynn-host plugin:install.
var CorePluginCommands []string

func IsCorePluginCommand(name string) bool {
	for _, cmd := range CorePluginCommands {
		if cmd == name {
			return true
		}
	}
	return false
}

func CLIFromApp(app *ct.App) *CLI {
	if app == nil || app.Meta == nil {
		return nil
	}
	raw := app.Meta[MetaPluginCLI]
	if raw == "" {
		return nil
	}
	var cli CLI
	if err := json.Unmarshal([]byte(raw), &cli); err != nil {
		return nil
	}
	return &cli
}
