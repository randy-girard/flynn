package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flynn/flynn/pkg/cliutil"
	"github.com/flynn/go-docopt"
)

func parseHostCLI(t *testing.T, name string, argv []string) *docopt.Args {
	t.Helper()
	cmd := commands[name]
	if cmd == nil {
		t.Fatalf("unknown flynn-host command %q", name)
	}
	parsed, err := docopt.Parse(cmd.usage, argv, true, "", strings.Contains(cmd.usage, "[--]"))
	if err != nil {
		t.Fatalf("parse %v: %v", argv, err)
	}
	return parsed
}

func TestHostCLIPositionalLists(t *testing.T) {
	stop := parseHostCLI(t, "stop", []string{"stop", "host-abc"})
	if got := cliutil.List(stop, "ID"); !reflect.DeepEqual(got, []string{"host-abc"}) {
		t.Fatalf("stop one ID: %q", got)
	}

	tags := parseHostCLI(t, "tags", []string{"tags", "del", "host0", "a", "b"})
	if got := cliutil.List(tags, "<var>"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("tags del: %q", got)
	}

	discover := parseHostCLI(t, "discover", []string{"discover", "controller"})
	if got := cliutil.List(discover, "<service>"); !reflect.DeepEqual(got, []string{"controller"}) {
		t.Fatalf("discover one service: %q", got)
	}

	fix := parseHostCLI(t, "fix", []string{"fix", "--yes"})
	if !fix.Bool["--yes"] {
		t.Fatal("fix --yes")
	}
	fixN := parseHostCLI(t, "fix", []string{"fix", "-n", "3", "--peer-ips", "10.0.0.1", "--yes"})
	if fixN.String["--min-hosts"] != "3" || fixN.String["--peer-ips"] != "10.0.0.1" {
		t.Fatalf("fix flags: %+v", fixN.String)
	}

	otel := parseHostCLI(t, "otel", []string{"otel", "add", "--scope", "system", "http://127.0.0.1:4318"})
	if otel.String["<endpoint>"] != "http://127.0.0.1:4318" || otel.String["--scope"] != "system" {
		t.Fatalf("otel add: %+v", otel.String)
	}

	domain := parseHostCLI(t, "domain", []string{"domain", "apex", "www"})
	if domain.String["<app>"] != "www" || !domain.Bool["apex"] {
		t.Fatalf("domain apex: %+v %+v", domain.String, domain.Bool)
	}

	sink := parseHostCLI(t, "log-sink", []string{"log-sink", "add", "syslog", "--scope", "apps", "syslog://127.0.0.1:514"})
	if sink.String["--scope"] != "apps" || sink.String["<url>"] != "syslog://127.0.0.1:514" {
		t.Fatalf("log-sink add: %+v", sink.String)
	}
}
