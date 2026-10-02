package plugin

import (
	"fmt"
	"strings"
)

// CLIScope is where a plugin CLI action lives and whether flynn -a / -r apply.
type CLIScope string

const (
	// CLIScopeApp acts on one app or an addon attached to it. Lives in flynn
	// and must honour global -a / -r / -c.
	CLIScopeApp CLIScope = "app"
	// CLIScopeAccount acts on the signed-in user's account. Lives in flynn,
	// ignores (and rejects) -a / -r; -c still selects the cluster.
	CLIScopeAccount CLIScope = "account"
	// CLIScopeCluster changes how the cluster itself behaves. Lives in
	// flynn-host under the same command name.
	CLIScopeCluster CLIScope = "cluster"
)

// EffectiveScope is the action's scope. An explicit scope wins; the legacy
// cluster flag is treated as cluster when scope is empty.
func (a *CLIAction) EffectiveScope() CLIScope {
	if a == nil {
		return CLIScopeApp
	}
	switch strings.ToLower(strings.TrimSpace(string(a.Scope))) {
	case string(CLIScopeApp):
		return CLIScopeApp
	case string(CLIScopeAccount):
		return CLIScopeAccount
	case string(CLIScopeCluster):
		return CLIScopeCluster
	}
	if a.Cluster {
		return CLIScopeCluster
	}
	return CLIScopeApp
}

// UsesSystemApp reports whether the job runs against the plugin system app
// instead of the user's current app.
func (a *CLIAction) UsesSystemApp() bool {
	s := a.EffectiveScope()
	return s == CLIScopeAccount || s == CLIScopeCluster
}

func (a *CLIAction) normalizeScope() error {
	if a == nil {
		return nil
	}
	s := strings.ToLower(strings.TrimSpace(string(a.Scope)))
	switch s {
	case "":
		if a.Cluster {
			a.Scope = CLIScopeCluster
		} else {
			a.Scope = CLIScopeApp
		}
	case string(CLIScopeApp), string(CLIScopeAccount), string(CLIScopeCluster):
		a.Scope = CLIScope(s)
		if a.Cluster && a.Scope != CLIScopeCluster {
			return fmt.Errorf("cluster:true conflicts with scope %q", a.Scope)
		}
	default:
		return fmt.Errorf("scope must be app, account, or cluster")
	}
	return nil
}

// HasFlynnVisibleActions is true when at least one action belongs on the
// laptop flynn CLI (app or account scope).
func (c *CLI) HasFlynnVisibleActions() bool {
	if c == nil {
		return false
	}
	for i := range c.Actions {
		if c.Actions[i].EffectiveScope() != CLIScopeCluster {
			return true
		}
	}
	return false
}

// HasClusterActions is true when at least one action belongs on flynn-host.
func (c *CLI) HasClusterActions() bool {
	if c == nil {
		return false
	}
	for i := range c.Actions {
		if c.Actions[i].EffectiveScope() == CLIScopeCluster {
			return true
		}
	}
	return false
}

// ActionByName finds the action whose Name matches, or whose tokens match.
func (c *CLI) ActionByName(name string) *CLIAction {
	name = strings.TrimSpace(name)
	if c == nil || name == "" {
		return nil
	}
	if a := c.Action(name); a != nil {
		return a
	}
	want := strings.Fields(name)
	for i := range c.Actions {
		a := &c.Actions[i]
		if sameStrings(strings.Fields(a.Name), want) {
			return a
		}
		if ColonName(c.Command, a.Name) == c.Command+":"+strings.Join(want, ":") {
			return a
		}
	}
	return nil
}

// HostRedirect is the flynn-host equivalent of a cluster-scoped action.
func (c *CLI) HostRedirect(action *CLIAction) string {
	cmd := ""
	if c != nil {
		cmd = strings.TrimSpace(c.Command)
	}
	if cmd == "" {
		return "flynn-host"
	}
	if action == nil {
		return "flynn-host " + cmd
	}
	verb := strings.TrimSpace(action.Name)
	if verb == "" || verb == "show" {
		return "flynn-host " + cmd
	}
	return "flynn-host " + cmd + " " + verb
}

// FlynnRedirect is the flynn equivalent of an app/account-scoped action.
func (c *CLI) FlynnRedirect(action *CLIAction) string {
	cmd := ""
	if c != nil {
		cmd = strings.TrimSpace(c.Command)
	}
	if cmd == "" {
		return "flynn"
	}
	if action == nil {
		return "flynn " + cmd
	}
	verb := strings.TrimSpace(action.Name)
	if verb == "" || verb == "show" {
		return "flynn " + cmd
	}
	return "flynn " + ColonName(cmd, verb)
}
