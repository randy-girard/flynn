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
		"db-runtime:remove", "db-runtime:allow-custom",
	} {
		if commands[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	if commands["runtime"] == nil || commands["runtime:create"] == nil {
		t.Fatal("app runtime commands missing")
	}
	help := FormatHelp("db-runtime")
	for _, want := range []string{"db-runtime:create", "db-runtime:update", "db-runtime:remove", "db-runtime:allow-custom", "not app process"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
	name, args, from := ResolveCommand("db-runtime", []string{"create", "--cpu", "100", "--memory", "128MB", "--disk", "1GB", "redis", "cache"})
	if name != "db-runtime:create" || from != "db-runtime create" {
		t.Fatalf("alias %q from=%q args=%q", name, from, args)
	}
}

func TestDBRuntimeCLIFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db-runtimes.json")
	t.Setenv(dbruntime.EnvFile, path)

	create := parseHostCLI(t, "db-runtime:create", []string{
		"db-runtime:create", "--memory", "128MB", "--cpu", "100", "--disk", "1GB", "redis", "cache",
	})
	if err := runDBRuntimeCreate(create); err != nil {
		t.Fatal(err)
	}
	cat, err := dbruntime.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rt, ok := cat.Find("redis", "cache")
	if !ok || rt.CPU != 100 || rt.Builtin {
		t.Fatalf("created %#v %v", rt, ok)
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
	if dbruntime.Path() != path {
		t.Fatalf("path %s", dbruntime.Path())
	}
}
