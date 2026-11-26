package cli

import (
	"fmt"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
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

type fakeHostReleaseClient map[string]*ct.Release

func (f fakeHostReleaseClient) GetAppRelease(app string) (*ct.Release, error) {
	if rel, ok := f[app]; ok {
		return rel, nil
	}
	return nil, fmt.Errorf("no release for %s", app)
}

// NEW-11/DATA-69: flynn-host <plugin>:maintenance runs the plugin API task
// without the release env, so the task had no key and the plugin API
// answered 401. The job carries the plugin's own key and nothing else.
func TestHostPluginAPITaskCarriesPluginKey(t *testing.T) {
	spec := &plugin.CLI{
		Command: "kafka",
		App:     "kafka-plugin",
		Actions: []plugin.CLIAction{{Name: "maintenance", Args: []string{"/bin/flynn-kafka-api", "task"}, Passthrough: true, Scope: plugin.CLIScopeCluster}},
	}
	client := fakeHostReleaseClient{"kafka-plugin": {ID: "rel-1", Env: map[string]string{"CONTROLLER_KEY": "plugin-key", "KAFKA_IMAGE_ID": "img"}}}
	req, err := hostPluginJob(client, spec, spec.Action("maintenance"), []string{"maintenance", "kafka-a"})
	if err != nil {
		t.Fatal(err)
	}
	if req.ReleaseID != "rel-1" || strings.Join(req.Args, " ") != "/bin/flynn-kafka-api task maintenance kafka-a" {
		t.Fatalf("job %+v", req)
	}
	if req.Env["CONTROLLER_KEY"] != "plugin-key" {
		t.Fatalf("plugin API task must carry the plugin key: %v", req.Env)
	}
	if req.ReleaseEnv || req.Env["KAFKA_IMAGE_ID"] != "" {
		t.Fatalf("only the key is copied: %+v", req)
	}

	// A non-API plugin binary gets no key.
	spec.Actions[0].Args = []string{"/bin/billing-cli"}
	req, err = hostPluginJob(client, spec, spec.Action("maintenance"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Env) != 0 {
		t.Fatalf("non-API task env %v", req.Env)
	}
}
