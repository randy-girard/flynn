package cli

import (
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/plugin"
)

func billingHostCLI() *plugin.CLI {
	return &plugin.CLI{
		Command: "billing",
		App:     "billing-plugin",
		Usage:   "Hosted plans and Stripe",
		Doc:     "usage: flynn billing\n       flynn billing:plans\n       flynn billing plans\n",
		Actions: []plugin.CLIAction{
			{Name: "show", Args: []string{"/bin/billing-cli"}, Passthrough: true, Scope: plugin.CLIScopeCluster},
			{Name: "plans", Args: []string{"/bin/billing-cli"}, Passthrough: true, Scope: plugin.CLIScopeCluster},
		},
	}
}

func TestSplitHostPluginCommand(t *testing.T) {
	name, args := splitHostPluginCommand("enterprise:license", []string{"KEY"})
	if name != "enterprise" || strings.Join(args, " ") != "license KEY" {
		t.Fatalf("%s %q", name, args)
	}
	name, args = splitHostPluginCommand("billing", []string{"plans"})
	if name != "billing" || strings.Join(args, " ") != "plans" {
		t.Fatalf("%s %q", name, args)
	}
}

func TestRunHostPluginCommandRedirectsFlynnActions(t *testing.T) {
	isolateInstalledPlugins(t, []plugin.Installed{{
		Name: "pipeline-plugin",
		CLI: &plugin.CLI{
			Command: "pipeline",
			App:     "pipeline-plugin",
			Doc:     "usage: flynn pipeline\n       flynn pipeline:create <name>\n",
			Actions: []plugin.CLIAction{
				{Name: "create", Args: []string{"/bin/pipeline-cli"}, Passthrough: true, Scope: plugin.CLIScopeAccount},
			},
		},
	}})
	err := runHostPluginCommand("pipeline", []string{"create", "shop"})
	if err == nil || !strings.Contains(err.Error(), "flynn pipeline") {
		t.Fatalf("got %v", err)
	}
}

func TestRunHostPluginCommandUnknownIsInvalid(t *testing.T) {
	isolateInstalledPlugins(t, nil)
	if err := runHostPluginCommand("not-a-plugin", nil); err != ErrInvalidCommand {
		t.Fatalf("got %v", err)
	}
}

func TestRootHelpListsInstalledClusterPluginCommands(t *testing.T) {
	isolateInstalledPlugins(t, []plugin.Installed{{
		Name: "billing-plugin",
		CLI:  billingHostCLI(),
	}})
	got := RootHelp()
	plugins := helpSectionNames(got, "Plugins:")
	if !containsName(plugins, "billing") {
		t.Fatalf("Plugins missing billing:\n%s", got)
	}
	if containsName(helpSectionNames(got, "Commands:"), "billing") {
		t.Fatalf("billing belongs under Plugins:\n%s", got)
	}
}

func TestFormatHelpHostPlugin(t *testing.T) {
	isolateInstalledPlugins(t, []plugin.Installed{{
		Name: "billing-plugin",
		CLI:  billingHostCLI(),
	}})
	got := FormatHelp("billing")
	if !strings.Contains(got, "usage: flynn-host billing") || !strings.Contains(got, "billing:plans") {
		t.Fatalf("billing help:\n%s", got)
	}
	if !KnownHelpTopic("billing") || !KnownHelpTopic("billing:plans") {
		t.Fatal("known topics")
	}
}

func TestExecuteHostPluginActionRequiresApp(t *testing.T) {
	spec := &plugin.CLI{Command: "billing", Actions: []plugin.CLIAction{{Name: "plans", Scope: plugin.CLIScopeCluster}}}
	err := executeHostPluginAction(nil, spec, spec.Action("plans"), nil)
	if err == nil || !strings.Contains(err.Error(), "plugin app name") {
		t.Fatalf("got %v", err)
	}
}
