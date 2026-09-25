package plugin

import (
	"fmt"
	"strings"
)

// ResolveGroupMembers resolves every plugin in a group. Private members such
// as billing are not skipped: they resolve only when plugins.json (and the
// existing plugin:credentials git auth) points at a cloneable repo.
func ResolveGroupMembers(name string, opts InstallOptions) (PluginGroup, []*Resolved, error) {
	g, ok := LookupGroup(name)
	if !ok {
		return PluginGroup{}, nil, fmt.Errorf("unknown plugin group %q", name)
	}
	out := make([]*Resolved, 0, len(g.Plugins))
	for _, member := range g.Plugins {
		memberOpts := opts
		memberOpts.Source = member
		resolved, err := Resolve(memberOpts)
		if err != nil {
			if member == "billing" || IsPrivatePluginName(member) {
				return g, nil, fmt.Errorf("plugin group %s: cannot resolve private plugin %s: add %s to plugins.json and configure plugin:credentials so plugin:install can authenticate to the private repository: %w", g.Name, member, member, err)
			}
			return g, nil, fmt.Errorf("plugin group %s: cannot resolve %s: %w", g.Name, member, err)
		}
		out = append(out, resolved)
	}
	return g, out, nil
}

// GroupTenancyMode is the cluster mode a successful group install applies.
// Empty means the group does not change tenancy mode. It never enables signup.
func GroupTenancyMode(name string) string {
	g, ok := LookupGroup(name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(g.TenancyMode)
}
