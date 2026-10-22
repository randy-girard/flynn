package cli

import (
	"strings"

	"github.com/randy-girard/flynn/pkg/clihelp"
	"github.com/randy-girard/flynn/pkg/plugin"
)

// hostPluginCommand maps a flynn-host command parent to the plugin that
// publishes it. Root help lists these under Plugins: only when installed.
var hostPluginCommand = map[string]string{
	"otel":        "otel",
	"letsencrypt": "letsencrypt",
	"acme":        "letsencrypt",
	"github":      "github",
	"alert":       "dashboard",
	"events":      "dashboard",
}

// hostPluginHelpSkip hides duplicate parents of the same plugin (acme is
// letsencrypt).
var hostPluginHelpSkip = map[string]struct{}{
	"acme": {},
}

func installedHostPlugins() map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range plugin.ReadInstalled("") {
		for _, n := range p.ResolveNames() {
			n = strings.ToLower(strings.TrimSpace(n))
			if n == "" {
				continue
			}
			out[n] = struct{}{}
		}
	}
	return out
}

func hostPluginForParent(name string) (pluginName string, ok bool) {
	parent := name
	if i := strings.IndexByte(name, ':'); i >= 0 {
		parent = name[:i]
	}
	pluginName, ok = hostPluginCommand[parent]
	if ok {
		return pluginName, true
	}
	if parentHasBuiltin(parent) {
		return "", false
	}
	if spec := installedHostPluginCLI(parent); spec != nil && spec.HasClusterActions() {
		return spec.Command, true
	}
	return "", false
}

// parentHasBuiltin is true when flynn-host already owns this command parent
// (pg:psql, for example). A plugin cluster action must not take that parent
// out of the core command list.
func parentHasBuiltin(parent string) bool {
	prefix := parent + ":"
	for name := range commands {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func installedClusterPluginCLIs() []*plugin.CLI {
	var out []*plugin.CLI
	seen := map[string]struct{}{}
	for _, p := range plugin.ReadInstalled("") {
		if p.CLI == nil || !p.CLI.HasClusterActions() {
			continue
		}
		cmd := strings.TrimSpace(p.CLI.Command)
		if cmd == "" {
			continue
		}
		if _, ok := seen[cmd]; ok {
			continue
		}
		seen[cmd] = struct{}{}
		cli := *p.CLI
		if cli.App == "" {
			cli.App = p.Name
		}
		out = append(out, &cli)
	}
	return out
}

// pluginClusterHelpItems lists flynn-host actions a plugin adds under a
// parent that already has built-in commands, such as pg:maintenance.
func pluginClusterHelpItems(parent string) []clihelp.Item {
	spec := installedHostPluginCLI(parent)
	if spec == nil {
		return nil
	}
	var items []clihelp.Item
	for i := range spec.Actions {
		a := &spec.Actions[i]
		if a.EffectiveScope() != plugin.CLIScopeCluster {
			continue
		}
		items = append(items, clihelp.Item{
			Name: plugin.ColonName(spec.Command, a.Name),
			Desc: "Run maintenance on resource instances",
		})
	}
	return items
}
