package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/cli"
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

func TestHostUsageCommandsAreAlphabetical(t *testing.T) {
	isolateInstalledPlugins(t, nil)
	assertHelpSectionAlphabetical(t, cli.RootHelp(), "Commands:")
}

func TestHostUsagePluginsAreAlphabetical(t *testing.T) {
	isolateInstalledPlugins(t, []plugin.Installed{
		{Name: "otel"},
		{Name: "letsencrypt"},
		{Name: "github"},
		{Name: "dashboard"},
	})
	got := cli.RootHelp()
	if !strings.Contains(got, "\nPlugins:") {
		t.Fatalf("expected Plugins section:\n%s", got)
	}
	assertHelpSectionAlphabetical(t, got, "Commands:")
	assertHelpSectionAlphabetical(t, got, "Plugins:")
}

func assertHelpSectionAlphabetical(t *testing.T, help, heading string) {
	t.Helper()
	var names []string
	in := false
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "See '") {
			break
		}
		if trimmed == heading {
			in = true
			continue
		}
		if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			if trimmed != "Options:" && trimmed != "Commands:" && trimmed != "Plugins:" {
				t.Fatalf("help must not group commands under %q", trimmed)
			}
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
	if len(names) < 2 {
		t.Fatalf("expected %s list", heading)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("%s not alphabetical: %v", heading, names)
	}
}
