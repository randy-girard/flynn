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
	return filterPluginUsage(usage, cat, err)
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
