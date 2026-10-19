package cli

import (
	"path/filepath"
	"reflect"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

func TestFormatDaemonTagsArg(t *testing.T) {
	got, err := formatDaemonTagsArg([]string{"role=database", "zone"}, "1")
	if err != nil {
		t.Fatal(err)
	}
	want := ct.FormationMaxProcessesTag + "=1,role=database,zone"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	empty, err := formatDaemonTagsArg(nil, "")
	if err != nil || empty != "" {
		t.Fatalf("empty: %q %v", empty, err)
	}

	cleared, err := formatDaemonTagsArg([]string{ct.FormationMaxProcessesTag + "=4"}, "unlimited")
	if err != nil || cleared != "" {
		t.Fatalf("unlimited should drop cap: %q %v", cleared, err)
	}

	if _, err := formatDaemonTagsArg(nil, "nope"); err == nil {
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
	want := []string{"--tags", ct.FormationMaxProcessesTag + "=1,role=database"}
	if !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("args=%v want %v", c.Args, want)
	}
}
