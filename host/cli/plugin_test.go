package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
)

func TestReadCredentialTokenFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  ghp_example  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	tok, err := readCredentialToken(path)
	if err != nil || tok != "ghp_example" {
		t.Fatalf("%q %v", tok, err)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialToken(empty); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v", err)
	}

	if _, err := readCredentialToken(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing file")
	}
}

func parsePluginCmd(t *testing.T, name string, argv ...string) *docopt.Args {
	t.Helper()
	cmd := commands[name]
	if cmd == nil {
		t.Fatalf("unknown command %q", name)
	}
	args, err := docopt.Parse(cmd.usage, argv, false, "", false)
	if err != nil {
		t.Fatalf("parse %s %q: %v", name, argv, err)
	}
	return args
}

func TestPluginRouteUsage(t *testing.T) {
	args := parsePluginCmd(t, "plugin:route", "plugin:route", "dashboard", "add", "http", "--auto-tls")
	if !args.Bool["add"] || !args.Bool["http"] || !args.Bool["--auto-tls"] {
		t.Fatalf("flags: %+v", args)
	}
	if args.String["<plugin>"] != "dashboard" {
		t.Fatalf("plugin=%q", args.String["<plugin>"])
	}
	if args.String["<domain>"] != "" {
		t.Fatalf("domain=%q", args.String["<domain>"])
	}

	args = parsePluginCmd(t, "plugin:route", "plugin:route", "dashboard", "add", "http", "--auto-tls", "dashboard.example.com")
	if args.String["<domain>"] != "dashboard.example.com" {
		t.Fatalf("domain=%q", args.String["<domain>"])
	}

	args = parsePluginCmd(t, "plugin:route", "plugin:route", "dashboard", "update", "http/abc", "--auto-tls")
	if !args.Bool["update"] || args.String["<id>"] != "http/abc" || !args.Bool["--auto-tls"] {
		t.Fatalf("update: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:route", "plugin:route", "dashboard")
	if args.Bool["add"] || args.Bool["update"] || args.Bool["remove"] {
		t.Fatalf("list: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:install", "plugin:install", "dashboard", "--yes")
	if !args.Bool["--yes"] || args.String["<source>"] != "dashboard" {
		t.Fatalf("install --yes: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:install", "plugin:install", "dashboard", "--auto-tls")
	if !args.Bool["--auto-tls"] || args.String["<source>"] != "dashboard" {
		t.Fatalf("install: %+v", args)
	}
	if args.Bool["--allow-external-layers"] {
		t.Fatalf("install default must not allow external layers: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:install", "plugin:install", "dashboard", "--allow-external-layers")
	if !args.Bool["--allow-external-layers"] || args.String["<source>"] != "dashboard" {
		t.Fatalf("install --allow-external-layers: %+v", args)
	}
}

func TestPluginUninstallUsage(t *testing.T) {
	args := parsePluginCmd(t, "plugin:uninstall", "plugin:uninstall", "redis")
	if args.Bool["--force"] {
		t.Fatalf("uninstall: %+v", args)
	}
	if args.String["<plugin>"] != "redis" {
		t.Fatalf("plugin=%q", args.String["<plugin>"])
	}

	args = parsePluginCmd(t, "plugin:uninstall", "plugin:uninstall", "--force", "redis")
	if !args.Bool["--force"] || args.String["<plugin>"] != "redis" {
		t.Fatalf("force: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:uninstall", "plugin:uninstall", "dashboard")
	if args.String["<plugin>"] != "dashboard" {
		t.Fatalf("dashboard uninstall must not parse as route: %+v", args)
	}
}

func TestPluginUpdateUsage(t *testing.T) {
	args := parsePluginCmd(t, "plugin:update", "plugin:update", "dashboard", "--yes")
	if !args.Bool["--yes"] || args.String["<plugin>"] != "dashboard" {
		t.Fatalf("update --yes: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:update", "plugin:update", "dashboard")
	if args.String["<plugin>"] != "dashboard" {
		t.Fatalf("plugin=%q", args.String["<plugin>"])
	}

	args = parsePluginCmd(t, "plugin:update", "plugin:update", "dashboard", "--ref", "v20260916.3.1")
	if args.String["--ref"] != "v20260916.3.1" || args.String["<plugin>"] != "dashboard" {
		t.Fatalf("update ref: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:update-all", "plugin:update-all")
	if args.Bool["--auto-tls"] {
		t.Fatalf("update-all: %+v", args)
	}
	args = parsePluginCmd(t, "plugin:update-all", "plugin:update-all", "--auto-tls")
	if !args.Bool["--auto-tls"] {
		t.Fatalf("update-all --auto-tls: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:update", "plugin:update", "dashboard", "--allow-external-layers")
	if !args.Bool["--allow-external-layers"] || args.String["<plugin>"] != "dashboard" {
		t.Fatalf("update --allow-external-layers: %+v", args)
	}
	args = parsePluginCmd(t, "plugin:update-all", "plugin:update-all", "--allow-external-layers")
	if !args.Bool["--allow-external-layers"] {
		t.Fatalf("update-all --allow-external-layers: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:update-all", "plugin:update-all", "--yes")
	if !args.Bool["--yes"] {
		t.Fatalf("update-all --yes: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:route", "plugin:route", "dashboard", "update", "http/abc")
	if !args.Bool["update"] || args.String["<id>"] != "http/abc" {
		t.Fatalf("route update must still parse: %+v", args)
	}
}

func TestPluginListKnownUsage(t *testing.T) {
	args := parsePluginCmd(t, "plugin:list", "plugin:list")
	if args.Bool["--known"] {
		t.Fatalf("list: %+v", args)
	}

	args = parsePluginCmd(t, "plugin:list", "plugin:list", "--known")
	if !args.Bool["--known"] {
		t.Fatalf("list --known: %+v", args)
	}
}
