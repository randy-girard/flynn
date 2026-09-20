package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/pkg/clihelp"
	"github.com/randy-girard/flynn/pkg/plugin"
)

const rootHelpFooter = `
See 'flynn help <command>' or 'flynn <command> --help' for a command and its subcommands.
`

func registeredHelpNames() []string {
	names := make([]string, 0, len(commands)+1)
	for n := range commands {
		names = append(names, n)
	}
	return names
}

func helpTopic(name string, args []string) string {
	if name == "" {
		return name
	}
	args = clihelp.StripHelp(args)
	all := registeredHelpNames()
	if catalogNames := catalogHelpNames(); len(catalogNames) > 0 {
		all = append(append([]string{}, all...), catalogNames...)
	}
	if clihelp.HasChildren(all, name) && len(args) == 0 {
		return name
	}
	if to, _ := pluginSpaceAlias(name, args); to != "" {
		return to
	}
	resolved, _, _ := resolveCommand(name, args)
	return resolved
}

func knownHelpTopic(name string) bool {
	if name == "" {
		return false
	}
	if commands[name] != nil {
		return true
	}
	if _, ok := topAliases[name]; ok {
		return true
	}
	all := append(registeredHelpNames(), catalogHelpNames()...)
	if clihelp.HasChildren(all, name) {
		return true
	}
	for _, n := range catalogHelpNames() {
		if n == name {
			return true
		}
	}
	cat, err := clusterPluginCatalog()
	return err == nil && cat != nil && cat.Lookup(name) != nil
}

func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func catalogHelpNames() []string {
	cat, err := clusterPluginCatalog()
	if err != nil || cat == nil {
		return nil
	}
	return catalogActionNames(cat)
}

func catalogActionNames(cat *plugin.Catalog) []string {
	if cat == nil {
		return nil
	}
	var names []string
	for _, cmd := range cat.Commands {
		if cmd.Command == "" || len(cmd.Actions) == 0 {
			continue
		}
		names = append(names, pluginActionNames(cmd)...)
	}
	return names
}

func commandUsage(name string) string {
	if cmd := commands[name]; cmd != nil {
		return cmd.usage
	}
	if target, ok := topAliases[name]; ok {
		if cmd := commands[target]; cmd != nil {
			return cmd.usage
		}
	}
	return ""
}

func formatCoreRootHelp() string {
	all := registeredHelpNames()
	parents := clihelp.Parents(all)
	items := make([]clihelp.Item, 0, len(parents)+1)
	seen := map[string]struct{}{}
	add := func(name, desc string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		if desc == "" {
			desc = name + " commands"
		}
		items = append(items, clihelp.Item{Name: name, Desc: desc})
	}
	add("help", "Show usage for a specific command")
	for _, name := range parents {
		add(name, clihelp.ShortDescription(commandUsage(name)))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return strings.TrimRight(cliUsage, "\n") + "\n\nCommands:\n" + clihelp.FormatItems(items) + rootHelpFooter
}

func formatRootHelp() string {
	return pluginAwareUsage(formatCoreRootHelp())
}

func formatHelp(name string) string {
	cat, _ := clusterPluginCatalog()
	return formatHelpWith(name, cat)
}

func formatHelpWith(name string, cat *plugin.Catalog) string {
	all := append(registeredHelpNames(), catalogActionNames(cat)...)
	if target, ok := topAliases[name]; ok && !clihelp.HasChildren(all, name) {
		name = target
	}
	var b strings.Builder
	if cmd := commands[name]; cmd != nil {
		b.WriteString(strings.TrimRight(cmd.usage, "\n"))
		b.WriteByte('\n')
	} else if spec := catalogLookupIn(cat, name); spec != nil && spec.Command == name {
		fmt.Fprintf(&b, "usage: flynn %s\n", name)
		if clihelp.HasChildren(all, name) {
			fmt.Fprintf(&b, "       flynn %s <command> [<args>...]\n", name)
		}
		if spec.Usage != "" {
			b.WriteByte('\n')
			b.WriteString(spec.Usage)
			b.WriteByte('\n')
		}
	} else if spec := owningPlugin(cat, name); spec != nil && spec.Doc != "" && !clihelp.HasChildren(all, name) {
		b.WriteString(strings.TrimRight(spec.Doc, "\n"))
		b.WriteByte('\n')
		return b.String()
	} else {
		fmt.Fprintf(&b, "usage: flynn %s\n", name)
		if clihelp.HasChildren(all, name) {
			fmt.Fprintf(&b, "       flynn %s <command> [<args>...]\n", name)
			b.WriteByte('\n')
		}
	}
	children := helpChildrenWith(name, cat)
	if len(children) == 0 || strings.Contains(b.String(), "\nCommands:") {
		return b.String()
	}
	b.WriteString("\nCommands:\n")
	items := make([]clihelp.Item, 0, len(children))
	for _, s := range children {
		items = append(items, clihelp.Item{Name: s.verb, Desc: s.desc})
	}
	b.WriteString(clihelp.FormatItems(items))
	return b.String()
}

type helpChild struct {
	verb string
	desc string
}

func helpChildrenWith(name string, cat *plugin.Catalog) []helpChild {
	all := append(registeredHelpNames(), catalogActionNames(cat)...)
	verbs := clihelp.Children(all, name)
	out := make([]helpChild, 0, len(verbs))
	for _, verb := range verbs {
		full := name + ":" + verb
		desc := clihelp.ShortDescription(commandUsage(full))
		if desc == "" {
			if spec := catalogLookupIn(cat, full); spec != nil {
				desc = spec.Usage
			} else if spec := owningPlugin(cat, full); spec != nil {
				desc = pluginActionDesc(spec, full)
			}
		}
		out = append(out, helpChild{verb: verb, desc: desc})
	}
	return out
}

func catalogLookupIn(cat *plugin.Catalog, name string) *plugin.CLI {
	if cat == nil {
		return nil
	}
	return cat.Lookup(name)
}

func owningPlugin(cat *plugin.Catalog, name string) *plugin.CLI {
	if spec := catalogLookupIn(cat, name); spec != nil {
		return spec
	}
	base, _, ok := splitColonCommand(name)
	if !ok {
		return nil
	}
	spec := catalogLookupIn(cat, base)
	if spec == nil {
		return nil
	}
	for _, n := range pluginActionNames(*spec) {
		if n == name || strings.HasPrefix(name, n+":") {
			return spec
		}
	}
	return nil
}

func pluginActionDesc(spec *plugin.CLI, full string) string {
	if spec == nil {
		return ""
	}
	for _, a := range spec.Actions {
		if pluginColonName(spec.Command, a.Name) == full {
			return a.Name
		}
	}
	return ""
}
