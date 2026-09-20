package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/pkg/plugin"
)

func clusterPluginCatalog() (*plugin.Catalog, error) {
	return fetchPluginCatalog()
}

func fetchClusterPluginCatalog() (*plugin.Catalog, error) {
	client, err := getClusterClient()
	if err != nil {
		return nil, err
	}
	return plugin.LoadCatalog(client)
}

// fetchPluginCatalog is the live cluster catalog. CLI tests replace it so
// help and usage checks cannot hang on ~/.flynnrc.
var fetchPluginCatalog = fetchClusterPluginCatalog

func requirePluginCommand(name string) error {
	if !plugin.IsCorePluginCommand(name) {
		return nil
	}
	cat, err := clusterPluginCatalog()
	return missingPluginCommand(name, cat, err)
}

func missingPluginCommand(name string, cat *plugin.Catalog, catErr error) error {
	if catErr != nil || cat == nil || !cat.HasCommand(name) {
		return fmt.Errorf("%s is not installed on this cluster. Operators: flynn-host plugin:install %s", name, name)
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
		desc := cmd.Usage
		if desc == "" {
			desc = "plugin command"
		}
		names := pluginHelpNames(cmd)
		for _, name := range names {
			if _, ok := present[name]; ok {
				continue
			}
			extra = append(extra, fmt.Sprintf("\t%-22s %s", name, desc))
			present[name] = struct{}{}
		}
	}
	if len(extra) == 0 {
		return usage
	}
	sort.Strings(extra)
	section := []string{"", "Plugins:"}
	section = append(section, extra...)
	section = append(section, "")
	return insertPluginHelpSection(usage, section)
}

// insertPluginHelpSection puts Plugins: after the core command list with a
// blank line before them, then restores the footer (See 'flynn help …').
func insertPluginHelpSection(usage string, section []string) string {
	lines := strings.Split(usage, "\n")
	out := make([]string, 0, len(lines)+len(section))
	inserted := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inserted && strings.HasPrefix(trimmed, "See '") {
			for len(out) > 0 && out[len(out)-1] == "" {
				out = out[:len(out)-1]
			}
			out = append(out, section...)
			inserted = true
		}
		out = append(out, line)
	}
	if !inserted {
		for len(out) > 0 && out[len(out)-1] == "" {
			out = out[:len(out)-1]
		}
		out = append(out, section...)
	}
	return strings.Join(out, "\n")
}

func pluginHelpNames(cmd plugin.CLI) []string {
	if cmd.Command == "" {
		return nil
	}
	return []string{cmd.Command}
}

// pluginActionNames is every colon command a plugin publishes (redis:dump).
func pluginActionNames(cmd plugin.CLI) []string {
	if len(cmd.Actions) == 0 {
		if cmd.Command == "" {
			return nil
		}
		return []string{cmd.Command}
	}
	names := make([]string, 0, len(cmd.Actions))
	seen := map[string]struct{}{}
	for _, a := range cmd.Actions {
		if strings.TrimSpace(a.Name) == "" {
			continue
		}
		name := pluginColonName(cmd.Command, a.Name)
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return []string{cmd.Command}
	}
	sort.Strings(names)
	return names
}

// usageCommandNames is every indented command in the help list. Scanning
// every line treated "See" from the footer as a command and could hide a
// plugin of that name.
func usageCommandNames(usage string) map[string]struct{} {
	present := map[string]struct{}{}
	for _, line := range strings.Split(usage, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "See '") {
			break
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			present[fields[0]] = struct{}{}
		}
	}
	return present
}
