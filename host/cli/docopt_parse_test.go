package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/cliutil"
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

	tags := parseHostCLI(t, "tags:del", []string{"tags:del", "host0", "a", "b"})
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

	otel := parseHostCLI(t, "otel:add", []string{"otel:add", "http://127.0.0.1:4318"})
	if otel.String["<endpoint>"] != "http://127.0.0.1:4318" {
		t.Fatalf("otel add: %+v", otel.String)
	}

	rt := parseHostCLI(t, "runtime-profile:create", []string{"runtime-profile:create", "--memory", "512MB", "--cpu", "500", "xlarge"})
	if rt.String["<name>"] != "xlarge" || rt.String["--memory"] != "512MB" || rt.String["--cpu"] != "500" {
		t.Fatalf("runtime-profile create: %+v", rt.String)
	}
	route := parseHostCLI(t, "route:add", []string{"route:add", "http", "--app", "admin", "example.com/admin"})
	if route.String["--app"] != "admin" || route.String["<domain>"] != "example.com/admin" {
		t.Fatalf("route add: %+v", route.String)
	}

	domain := parseHostCLI(t, "domain:apex", []string{"domain:apex", "www"})
	if domain.String["<app>"] != "www" {
		t.Fatalf("domain apex: %+v %+v", domain.String, domain.Bool)
	}

	sink := parseHostCLI(t, "log-sink:add", []string{"log-sink:add", "syslog", "--scope", "apps", "syslog://127.0.0.1:514"})
	if sink.String["--scope"] != "apps" || sink.String["<url>"] != "syslog://127.0.0.1:514" {
		t.Fatalf("log-sink add: %+v", sink.String)
	}
}
