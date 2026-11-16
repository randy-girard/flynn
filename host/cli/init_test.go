package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

func TestFormatDaemonTagsArg(t *testing.T) {
	got, err := formatDaemonTagsArg([]string{"role=database", "zone"}, "1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ct.FormationMaxProcessesTag + "=1," + ct.FormationMaxProcessesSourceTag + "=operator,role=database,zone"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	fresh, err := formatDaemonTagsArg(nil, "", "", nil)
	wantFresh := ct.FormationMaxProcessesTag + "=" + ct.HostMaxProcessesAuto + "," + ct.FormationMaxProcessesSourceTag + "=" + ct.HostMaxProcessesSourceAuto
	if err != nil || fresh != wantFresh {
		t.Fatalf("fresh: %q %v", fresh, err)
	}

	cleared, err := formatDaemonTagsArg([]string{ct.FormationMaxProcessesTag + "=4"}, "unlimited", "", nil)
	if err != nil || cleared != "" {
		t.Fatalf("unlimited should drop cap: %q %v", cleared, err)
	}

	if _, err := formatDaemonTagsArg(nil, "nope", "", nil); err == nil {
		t.Fatal("expected invalid --max-processes")
	}
}

func TestInitParsesPlacementFlags(t *testing.T) {
	args := parseHostCLI(t, "init", []string{
		"init",
		"--peer-ips", "10.0.0.1",
		"--tag", "role=database",
		"--tag", "zone=a",
		"--max-processes", "1",
	})
	if args.String["--peer-ips"] != "10.0.0.1" {
		t.Fatalf("peer-ips: %+v", args.String)
	}
	if args.String["--max-processes"] != "1" {
		t.Fatalf("max-processes: %+v", args.String)
	}
	if got := cliutil.List(args, "--tag"); len(got) != 2 || got[0] != "role=database" || got[1] != "zone=a" {
		t.Fatalf("tags: %q", got)
	}
}

func TestRunInitWritesDaemonTags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	args := parseHostCLI(t, "init", []string{
		"init",
		"--file", path,
		"--tag", "role=database",
		"--max-processes", "1",
	})
	if err := runInit(args); err != nil {
		t.Fatal(err)
	}
	c, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--tags", ct.FormationMaxProcessesTag + "=1," + ct.FormationMaxProcessesSourceTag + "=operator,role=database"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("args=%v want %v", c.Args, want)
	}
}

func TestRunInitRoleWritesFlynnRoleAndKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	args := parseHostCLI(t, "init", []string{
		"init",
		"--file", path,
		"--tag", "role=database",
		"--role", "core",
		"--max-processes", "1",
	})
	if err := runInit(args); err != nil {
		t.Fatal(err)
	}
	c, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--tags", ct.FormationMaxProcessesTag + "=1," + ct.FormationMaxProcessesSourceTag + "=operator," + placement.TagRole + "=core,role=database"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("args=%v want %v", c.Args, want)
	}

	again := parseHostCLI(t, "init", []string{"init", "--file", path})
	if err := runInit(again); err != nil {
		t.Fatal(err)
	}
	c, err = config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	kept := []string{"--tags", ct.FormationMaxProcessesTag + "=1," + ct.FormationMaxProcessesSourceTag + "=operator," + placement.TagRole + "=core"}
	if !reflect.DeepEqual(c.Args, kept) {
		t.Fatalf("re-init args=%v want %v", c.Args, kept)
	}
}

func TestFormatDaemonTagsArgRole(t *testing.T) {
	app, err := formatDaemonTagsArg([]string{"zone=a"}, "", "app", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(app, placement.TagRole+"=app") || strings.Contains(","+app, ",role=") {
		t.Fatalf("app role tags %q", app)
	}
	if _, err := formatDaemonTagsArg(nil, "", "database", nil); err == nil || !strings.Contains(err.Error(), "--role") {
		t.Fatalf("invalid role: %v", err)
	}
	if _, err := formatDaemonTagsArg([]string{placement.TagRole + "=app"}, "", "core", nil); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict: %v", err)
	}
	same, err := formatDaemonTagsArg([]string{placement.TagRole + "=core"}, "", "core", nil)
	if err != nil || !strings.Contains(same, placement.TagRole+"=core") {
		t.Fatalf("same role: %q %v", same, err)
	}
}
