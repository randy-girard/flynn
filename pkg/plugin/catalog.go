package plugin

import (
	"encoding/json"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
)

// Catalog is the cluster's installed plugin CLI commands. Built from plugin
// app metadata (any kind) plus resource providers. Not a hardcoded appliance list.
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
	for _, cmd := range c.Commands {
		if cmd.Command == name {
			return true
		}
	}
	return false
}

// LoadCatalog lists plugin apps and providers on the cluster.
func LoadCatalog(client controller.Client) (*Catalog, error) {
	apps, err := client.AppList()
	if err != nil {
		return nil, err
	}
	providers, err := client.ProviderList()
	if err != nil {
		return catalogFrom(apps, nil), nil
	}
	return catalogFrom(apps, providers), nil
}

func catalogFrom(apps []*ct.App, providers []*ct.Provider) *Catalog {
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
		if _, ok := seen[cli.Command]; ok {
			continue
		}
		seen[cli.Command] = struct{}{}
		cli.App = app.Name
		cat.Commands = append(cat.Commands, cli)
	}

	for _, p := range providers {
		if p == nil || p.Name == "" {
			continue
		}
		if _, ok := seen[p.Name]; ok {
			continue
		}
		// Providers without a cli block still unlock a same-named flynn command
		// for compiled-in handlers (mysql, …). Fully extracted plugins must
		// stamp a runnable CLI spec (doc + actions) at install.
		seen[p.Name] = struct{}{}
		cat.Commands = append(cat.Commands, CLI{Command: p.Name})
	}
	return cat
}

// CorePluginCommands are compiled-in flynn handlers for plugins that are not
// yet fully extracted (no CLI actions in the cluster catalog). Help and
// dispatch hide them unless the catalog lists the command. Redis is not listed:
// its syntax lives on the plugin and the CLI delegates jobs after install.
var CorePluginCommands = []string{
	"mysql",
	"mongodb",
	"kafka",
	"clickhouse",
}

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
