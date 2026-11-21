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

// CORE-40: a host without a key accepts a remote POST /host/auth-key only
// with the join token that init writes into host.json.
func TestRunInitWritesJoinToken(t *testing.T) {
	for _, k := range []string{"FLYNN_HOST_AUTH_KEY", "DISCOVERD_AUTH_KEY", "AUTH_KEY", "CONTROLLER_KEY", "FLYNN_HOST_JOIN_TOKEN"} {
		t.Setenv(k, "")
	}
	path := filepath.Join(t.TempDir(), "host.json")
	args := parseHostCLI(t, "init", []string{"init", "--file", path, "--join-token", "0123456789abcdef0123"})
	if err := runInit(args); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.LoadEnvKey(path, "FLYNN_HOST_JOIN_TOKEN"); got != "0123456789abcdef0123" {
		t.Fatalf("--join-token not in host.json: %q", got)
	}

	// The exported variable works too (install scripts).
	path2 := filepath.Join(t.TempDir(), "host.json")
	t.Setenv("FLYNN_HOST_JOIN_TOKEN", "fedcba9876543210fedc")
	if err := runInit(parseHostCLI(t, "init", []string{"init", "--file", path2})); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.LoadEnvKey(path2, "FLYNN_HOST_JOIN_TOKEN"); got != "fedcba9876543210fedc" {
		t.Fatalf("FLYNN_HOST_JOIN_TOKEN not in host.json: %q", got)
	}

	// A short token is refused.
	t.Setenv("FLYNN_HOST_JOIN_TOKEN", "")
	err := runInit(parseHostCLI(t, "init", []string{"init", "--file", filepath.Join(t.TempDir(), "host.json"), "--join-token", "short"}))
	if err == nil || !strings.Contains(err.Error(), "join token") {
		t.Fatalf("short join token: %v", err)
	}
}
