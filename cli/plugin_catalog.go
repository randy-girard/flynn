package main

import (
	"fmt"
	"strings"

	"github.com/flynn/flynn/pkg/plugin"
)

func clusterPluginCatalog() (*plugin.Catalog, error) {
	client, err := getClusterClient()
	if err != nil {
		return nil, err
	}
	return plugin.LoadCatalog(client)
}

func requirePluginCommand(name string) error {
	if !plugin.IsCorePluginCommand(name) {
		return nil
	}
	cat, err := clusterPluginCatalog()
	return missingPluginCommand(name, cat, err)
}

func missingPluginCommand(name string, cat *plugin.Catalog, catErr error) error {
	if catErr != nil || cat == nil || !cat.HasCommand(name) {
		return fmt.Errorf("%s is not installed on this cluster. Operators: flynn-host plugin install %s", name, name)
	}
	return nil
}

func hideUnavailablePluginCommands(usage string) string {
	cat, err := clusterPluginCatalog()
	return mergePluginUsage(usage, cat, err)
}

func pluginAwareUsage(usage string) string {
	return hideUnavailablePluginCommands(usage)
}

func mergePluginUsage(usage string, cat *plugin.Catalog, catErr error) string {
	return appendCatalogCommands(filterPluginUsage(usage, cat, catErr), cat, catErr)
}

func filterPluginUsage(usage string, cat *plugin.Catalog, catErr error) string {
	lines := strings.Split(usage, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && plugin.IsCorePluginCommand(fields[0]) {
			if catErr != nil || cat == nil || !cat.HasCommand(fields[0]) {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func appendCatalogCommands(usage string, cat *plugin.Catalog, catErr error) string {
	if catErr != nil || cat == nil {
		return usage
	}
	present := usageCommandNames(usage)
	var extra []string
	for _, cmd := range cat.Commands {
		if cmd.Command == "" {
			continue
		}
		if !cmd.Runnable() {
			continue
		}
		if _, ok := present[cmd.Command]; ok {
			continue
		}
		desc := cmd.Usage
		if desc == "" {
			desc = "plugin command"
		}
		extra = append(extra, fmt.Sprintf("\t%-11s %s", cmd.Command, desc))
		present[cmd.Command] = struct{}{}
	}
	if len(extra) == 0 {
		return usage
	}
	lines := strings.Split(usage, "\n")
	out := make([]string, 0, len(lines)+len(extra))
	inserted := false
	for _, line := range lines {
		if !inserted && strings.HasPrefix(strings.TrimSpace(line), "See 'flynn help") {
			out = append(out, extra...)
			inserted = true
		}
		out = append(out, line)
	}
	if !inserted {
		out = append(out, extra...)
	}
	return strings.Join(out, "\n")
}

// usageCommandNames is the Commands: list only. Scanning every line treated
// "See" from the footer as a command and could hide a plugin of that name.
func usageCommandNames(usage string) map[string]struct{} {
	present := map[string]struct{}{}
	inCommands := false
	for _, line := range strings.Split(usage, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "Commands:" {
			inCommands = true
			continue
		}
		if !inCommands {
			continue
		}
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			present[fields[0]] = struct{}{}
		}
	}
	return present
}
