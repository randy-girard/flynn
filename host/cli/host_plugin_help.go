package cli

import (
	"strings"

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
	return pluginName, ok
}
