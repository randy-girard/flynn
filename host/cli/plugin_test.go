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

func parsePluginUsage(t *testing.T, argv ...string) *docopt.Args {
	t.Helper()
	args, err := docopt.Parse(pluginUsage, argv, false, "", false)
	if err != nil {
		t.Fatalf("parse %q: %v", argv, err)
	}
	return args
}

func TestPluginRouteUsage(t *testing.T) {
	args := parsePluginUsage(t, "plugin", "dashboard", "route", "add", "http", "--auto-tls")
	if !args.Bool["route"] || !args.Bool["add"] || !args.Bool["http"] || !args.Bool["--auto-tls"] {
		t.Fatalf("flags: %+v", args)
	}
	if args.String["<plugin>"] != "dashboard" {
		t.Fatalf("plugin=%q", args.String["<plugin>"])
	}
	if args.String["<domain>"] != "" {
		t.Fatalf("domain=%q", args.String["<domain>"])
	}

	args = parsePluginUsage(t, "plugin", "dashboard", "route", "add", "http", "--auto-tls", "dashboard.example.com")
	if args.String["<domain>"] != "dashboard.example.com" {
		t.Fatalf("domain=%q", args.String["<domain>"])
	}

	args = parsePluginUsage(t, "plugin", "dashboard", "route", "update", "http/abc", "--auto-tls")
	if !args.Bool["update"] || args.String["<id>"] != "http/abc" || !args.Bool["--auto-tls"] {
		t.Fatalf("update: %+v", args)
	}

	args = parsePluginUsage(t, "plugin", "dashboard", "route")
	if !args.Bool["route"] || args.Bool["add"] || args.Bool["install"] {
		t.Fatalf("list: %+v", args)
	}

	args = parsePluginUsage(t, "plugin", "install", "dashboard", "--auto-tls")
	if !args.Bool["install"] || args.Bool["route"] || args.String["<source>"] != "dashboard" {
		t.Fatalf("install: %+v", args)
	}
}

func TestPluginUninstallUsage(t *testing.T) {
	args := parsePluginUsage(t, "plugin", "uninstall", "redis")
	if !args.Bool["uninstall"] || args.Bool["install"] || args.Bool["route"] || args.Bool["--force"] {
		t.Fatalf("uninstall: %+v", args)
	}
	if args.String["<plugin>"] != "redis" {
		t.Fatalf("plugin=%q", args.String["<plugin>"])
	}

	args = parsePluginUsage(t, "plugin", "uninstall", "--force", "redis")
	if !args.Bool["uninstall"] || !args.Bool["--force"] || args.String["<plugin>"] != "redis" {
		t.Fatalf("force: %+v", args)
	}

	args = parsePluginUsage(t, "plugin", "uninstall", "dashboard")
	if !args.Bool["uninstall"] || args.Bool["route"] || args.String["<plugin>"] != "dashboard" {
		t.Fatalf("dashboard uninstall must not parse as route: %+v", args)
	}
}

func TestPluginUpdateUsage(t *testing.T) {
	args := parsePluginUsage(t, "plugin", "update", "dashboard")
	if !args.Bool["update"] || args.Bool["install"] || args.Bool["route"] || args.Bool["uninstall"] {
		t.Fatalf("update: %+v", args)
	}
	if args.String["<plugin>"] != "dashboard" {
		t.Fatalf("plugin=%q", args.String["<plugin>"])
	}

	args = parsePluginUsage(t, "plugin", "update", "dashboard", "--ref", "v20260916.3")
	if !args.Bool["update"] || args.String["--ref"] != "v20260916.3" || args.String["<plugin>"] != "dashboard" {
		t.Fatalf("update ref: %+v", args)
	}

	args = parsePluginUsage(t, "plugin", "dashboard", "route", "update", "http/abc")
	if !args.Bool["route"] || !args.Bool["update"] || args.String["<id>"] != "http/abc" {
		t.Fatalf("route update must still parse: %+v", args)
	}
}
