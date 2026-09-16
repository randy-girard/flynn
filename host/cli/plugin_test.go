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
