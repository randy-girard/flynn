package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/plugin"
)

func isolateInstalledPlugins(t *testing.T, plugins []plugin.Installed) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "installed-plugins.json")
	if err := plugin.WriteInstalled(path, plugins); err != nil {
		t.Fatal(err)
	}
	t.Setenv(plugin.EnvInstalledFile, path)
}

func helpSectionNames(help, heading string) []string {
	var names []string
	in := false
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == heading {
			in = true
			continue
		}
		if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			in = false
			continue
		}
		if !in || trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestFormatHelpListsNamespaceCommands(t *testing.T) {
	got := FormatHelp("otel")
	for _, want := range []string{"usage: flynn-host otel", "Commands:", "otel:add", "otel:remove", "OpenTelemetry"} {
		if !strings.Contains(got, want) {
			t.Fatalf("otel help missing %q:\n%s", want, got)
		}
	}
	plugin := FormatHelp("plugin")
	for _, want := range []string{"Commands:", "plugin:install", "plugin:uninstall", "plugin:update", "plugin:update-all", "plugin:route", "plugin:credentials", "plugin:list"} {
		if !strings.Contains(plugin, want) {
			t.Fatalf("plugin help missing %q:\n%s", want, plugin)
		}
	}
	if strings.Contains(plugin, "credentials-set") || strings.Contains(plugin, "credentials set") {
		t.Fatalf("plugin help should list the credentials namespace, not hyphen verbs:\n%s", plugin)
	}
	if strings.Contains(plugin, "plugin:credentials:set") {
		t.Fatalf("plugin help should not list grandchildren:\n%s", plugin)
	}
	list := FormatHelp("plugin:list")
	if strings.Contains(list, "\nCommands:") {
		t.Fatalf("plugin:list is a leaf:\n%s", list)
	}
	creds := FormatHelp("plugin:credentials")
	for _, want := range []string{"plugin:credentials:set", "plugin:credentials:unset", "plugin:credentials:show"} {
		if !strings.Contains(creds, want) {
			t.Fatalf("plugin:credentials help missing %q:\n%s", want, creds)
		}
	}
	fw := FormatHelp("firewall")
	for _, want := range []string{"firewall:peer:add", "firewall:peer:remove", "firewall:expose", "firewall:sync", "firewall:unexpose"} {
		if !strings.Contains(fw, want) {
			t.Fatalf("firewall help missing %q:\n%s", want, fw)
		}
	}
	if strings.Contains(fw, "peer-add") {
		t.Fatalf("firewall help should list firewall:peer:add, not peer-add:\n%s", fw)
	}
	alert := FormatHelp("alert")
	for _, want := range []string{"usage: flynn-host alert", "Commands:", "alert:add", "alert:enable", "alert:disable", "alert:remove"} {
		if !strings.Contains(alert, want) {
			t.Fatalf("alert help missing %q:\n%s", want, alert)
		}
	}
	add := FormatHelp("otel:add")
	if strings.Contains(add, "\nCommands:") {
		t.Fatalf("otel:add should not list the namespace:\n%s", add)
	}
	if !strings.Contains(add, "--auth") {
		t.Fatalf("otel:add help missing --auth:\n%s", add)
	}
	le := FormatHelp("letsencrypt")
	for _, want := range []string{"usage: flynn-host letsencrypt", "Commands:", "letsencrypt:configure", "letsencrypt:enable-system-routes"} {
		if !strings.Contains(le, want) {
			t.Fatalf("letsencrypt help missing %q:\n%s", want, le)
		}
	}
	bs := FormatHelp("blobstore")
	for _, want := range []string{"usage: flynn-host blobstore", "Commands:", "blobstore:status", "blobstore:set", "blobstore:credentials", "blobstore:migrate"} {
		if !strings.Contains(bs, want) {
			t.Fatalf("blobstore help missing %q:\n%s", want, bs)
		}
	}
}

func TestRootHelpListsParentsOnly(t *testing.T) {
	isolateInstalledPlugins(t, nil)
	got := RootHelp()
	commands := helpSectionNames(got, "Commands:")
	for _, want := range []string{"plugin", "volume", "blobstore", "help"} {
		if !containsName(commands, want) {
			t.Fatalf("root Commands missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\nPlugins:") {
		t.Fatalf("root help must not list Plugins: when none are installed:\n%s", got)
	}
	for _, pluginCmd := range []string{"otel", "letsencrypt", "acme", "github", "alert", "events"} {
		if containsName(commands, pluginCmd) {
			t.Fatalf("uninstalled plugin command %q must not appear in Commands:\n%s", pluginCmd, got)
		}
	}
	for _, nested := range []string{"plugin:install", "plugin:list", "otel:add", "volume:gc", "acme:configure", "letsencrypt:configure", "blobstore:set"} {
		if strings.Contains(got, nested) {
			t.Fatalf("root help should not list %q:\n%s", nested, got)
		}
	}
	if strings.Contains(got, "Example:") {
		t.Fatalf("root help leaked an example heading:\n%s", got)
	}
	if !strings.Contains(got, "Lists ID and IP of each host") {
		t.Fatalf("list summary missing:\n%s", got)
	}
}

func TestRootHelpPluginsSection(t *testing.T) {
	isolateInstalledPlugins(t, []plugin.Installed{
		{Name: "otel"},
		{Name: "letsencrypt"},
		{Name: "github"},
	})
	got := RootHelp()
	commands := helpSectionNames(got, "Commands:")
	plugins := helpSectionNames(got, "Plugins:")
	if !strings.Contains(got, "\nPlugins:") {
		t.Fatalf("root help missing Plugins section:\n%s", got)
	}
	for _, want := range []string{"otel", "letsencrypt", "github"} {
		if !containsName(plugins, want) {
			t.Fatalf("Plugins missing %q:\n%s", want, got)
		}
		if containsName(commands, want) {
			t.Fatalf("%q belongs under Plugins, not Commands:\n%s", want, got)
		}
	}
	if containsName(plugins, "acme") {
		t.Fatalf("acme is a letsencrypt alias and must not appear:\n%s", got)
	}
	for _, core := range []string{"plugin", "volume", "help"} {
		if !containsName(commands, core) {
			t.Fatalf("Commands missing core %q:\n%s", core, got)
		}
		if containsName(plugins, core) {
			t.Fatalf("core %q must stay in Commands:\n%s", core, got)
		}
	}
	for _, hidden := range []string{"alert", "events"} {
		if containsName(plugins, hidden) || containsName(commands, hidden) {
			t.Fatalf("dashboard command %q listed without dashboard installed:\n%s", hidden, got)
		}
	}
}

func TestRootHelpDashboardPluginCommands(t *testing.T) {
	isolateInstalledPlugins(t, []plugin.Installed{{Name: "dashboard"}})
	got := RootHelp()
	plugins := helpSectionNames(got, "Plugins:")
	for _, want := range []string{"alert", "events"} {
		if !containsName(plugins, want) {
			t.Fatalf("dashboard Plugins missing %q:\n%s", want, got)
		}
	}
	if containsName(plugins, "otel") || containsName(helpSectionNames(got, "Commands:"), "otel") {
		t.Fatalf("otel listed without otel installed:\n%s", got)
	}
}

func TestHelpTopicKeepsNamespaceRoots(t *testing.T) {
	if got := HelpTopic("plugin", []string{"--help"}); got != "plugin" {
		t.Fatalf("plugin --help: %q", got)
	}
	if got := HelpTopic("plugin", nil); got != "plugin" {
		t.Fatalf("plugin: %q", got)
	}
	if got := HelpTopic("plugin:install", []string{"--help"}); got != "plugin:install" {
		t.Fatalf("plugin:install --help: %q", got)
	}
	if got := HelpTopic("otel", []string{"add", "--help"}); got != "otel:add" {
		t.Fatalf("otel add --help: %q", got)
	}
}

func TestAllHostCommandsHaveHelp(t *testing.T) {
	for name, cmd := range commands {
		if strings.TrimSpace(cmd.usage) == "" {
			t.Errorf("%s has empty usage", name)
		}
		got := FormatHelp(name)
		if !strings.Contains(got, "usage: flynn-host "+name) && hyphenAliases[name] == "" {
			t.Errorf("%s help missing usage line:\n%s", name, got)
		}
	}
}

func TestWantsHelp(t *testing.T) {
	if !WantsHelp([]string{"--help"}) || !WantsHelp([]string{"-h"}) || !WantsHelp([]string{"add", "--help"}) {
		t.Fatal("expected help")
	}
	if WantsHelp(nil) || WantsHelp([]string{"add", "http://x"}) {
		t.Fatal("did not expect help")
	}
}
