package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/dbruntime"
)

func TestDBRuntimeCommandsRegistered(t *testing.T) {
	for _, name := range []string{
		"db-runtime", "db-runtime:create", "db-runtime:update",
		"db-runtime:remove", "db-runtime:ensure", "db-runtime:drop-engine",
		"db-runtime:allow-custom",
	} {
		if commands[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	if commands["runtime"] == nil || commands["runtime:create"] == nil || commands["runtime:max-restarts"] == nil {
		t.Fatal("app runtime commands missing")
	}
	help := FormatHelp("db-runtime")
	for _, want := range []string{"db-runtime:create", "db-runtime:update", "db-runtime:remove", "db-runtime:ensure", "db-runtime:drop-engine", "db-runtime:allow-custom", "not app process"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
	name, args, from := ResolveCommand("db-runtime", []string{"create", "--cpu", "100", "--memory", "128MB", "--disk", "1GB", "redis", "cache"})
	if name != "db-runtime:create" || from != "db-runtime create" {
		t.Fatalf("alias %q from=%q args=%q", name, from, args)
	}
}

func TestParseMaxRestarts(t *testing.T) {
	for in, want := range map[string]int{"unlimited": 0, "Unlimited": 0, "1": 1, " 10 ": 10} {
		got, err := parseMaxRestarts(in)
		if err != nil || got != want {
			t.Errorf("parseMaxRestarts(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "-1", "ten", ""} {
		if _, err := parseMaxRestarts(in); err == nil {
			t.Errorf("parseMaxRestarts(%q) accepted", in)
		}
	}
	if formatMaxRestarts(0) != "unlimited" || formatMaxRestarts(5) != "5" {
		t.Fatal("formatMaxRestarts")
	}
}

func TestFormatJobRestartInterval(t *testing.T) {
	for in, want := range map[string]string{"": "24h", "72h": "72h", "0": "off", "off": "off", "bogus": "24h"} {
		if got := formatJobRestartInterval(in); got != want {
			t.Errorf("formatJobRestartInterval(%q) = %q, want %q", in, got, want)
		}
	}
	if commands["runtime:job-restart-interval"] == nil {
		t.Fatal("runtime:job-restart-interval not registered")
	}
}

func TestDBRuntimeCLIFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db-runtimes.json")
	t.Setenv(dbruntime.EnvFile, path)

	ensure := parseHostCLI(t, "db-runtime:ensure", []string{"db-runtime:ensure", "redis"})
	if err := runDBRuntimeEnsure(ensure); err != nil {
		t.Fatal(err)
	}
	create := parseHostCLI(t, "db-runtime:create", []string{
		"db-runtime:create", "--memory", "128MB", "--cpu", "100", "--disk", "1GB", "redis", "cache",
	})
	if err := runDBRuntimeCreate(create); err != nil {
		t.Fatal(err)
	}
	dedicated := parseHostCLI(t, "db-runtime:create", []string{
		"db-runtime:create", "--memory", "1GB", "--cpu", "500", "--disk", "10GB", "--dedicated", "--tag", "size=small", "redis", "db-small",
	})
	if err := runDBRuntimeCreate(dedicated); err != nil {
		t.Fatal(err)
	}
	cat, err := dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rt, ok := cat.Find("redis", "cache")
	if !ok || rt.CPU != 100 || rt.Builtin || rt.Dedicated {
		t.Fatalf("created %#v %v", rt, ok)
	}
	rt, ok = cat.Find("redis", "db-small")
	if !ok || !rt.Dedicated || rt.Tags["size"] != "small" {
		t.Fatalf("dedicated %#v %v", rt, ok)
	}
	if err := runDBRuntimeUpdate(parseHostCLI(t, "db-runtime:update", []string{"db-runtime:update", "--shared", "redis", "db-small"})); err != nil {
		t.Fatal(err)
	}
	cat, err = dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rt, _ = cat.Find("redis", "db-small")
	if rt.Dedicated {
		t.Fatal("shared did not clear dedicated")
	}
	before, err := dbruntime.Resolve("redis", "small", cat, false)
	if err != nil {
		t.Fatal(err)
	}
	update := parseHostCLI(t, "db-runtime:update", []string{
		"db-runtime:update", "--disk", "2GB", "redis", "small",
	})
	if err := runDBRuntimeUpdate(update); err != nil {
		t.Fatal(err)
	}
	cat, err = dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	small, _ := cat.Find("redis", "small")
	if small.Disk == before.Disk {
		t.Fatal("definition disk did not change")
	}
	if got := dbruntime.SizeAfterDefinitionUpdate(before, small); got != before {
		t.Fatalf("instance size changed %#v", got)
	}
	if err := runDBRuntimeRemove(parseHostCLI(t, "db-runtime:remove", []string{"db-runtime:remove", "redis", "small"})); err == nil {
		t.Fatal("removed builtin")
	}
	if err := runDBRuntimeRemove(parseHostCLI(t, "db-runtime:remove", []string{"db-runtime:remove", "redis", "cache"})); err != nil {
		t.Fatal(err)
	}
	if err := runDBRuntimeAllowCustom(parseHostCLI(t, "db-runtime:allow-custom", []string{"db-runtime:allow-custom"})); err != nil {
		t.Fatal(err)
	}
	cat, err = dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cat.AllowCustomSizes {
		t.Fatal("allow custom")
	}
	if _, ok := cat.Find("redis", "cache"); ok {
		t.Fatal("cache still in file")
	}
	if err := runDBRuntimeDropEngine(parseHostCLI(t, "db-runtime:drop-engine", []string{"db-runtime:drop-engine", "redis"})); err != nil {
		t.Fatal(err)
	}
	cat, err = dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Find("redis", "small"); ok {
		t.Fatal("drop-engine left redis runtimes")
	}
	if dbruntime.Path() != path {
		t.Fatalf("path %s", dbruntime.Path())
	}
}
