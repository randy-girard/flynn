package main

import (
	"reflect"
	"testing"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/pkg/cliutil"
)

func parseCLI(t *testing.T, argv []string) *docopt.Args {
	t.Helper()
	name, rest, _ := resolveCommand(argv[0], argv[1:])
	cmd := commands[name]
	if cmd == nil {
		t.Fatalf("unknown command %q (resolved %q)", argv[0], name)
	}
	parsed, err := docopt.Parse(cmd.usage, append([]string{name}, rest...), true, "", cmd.optsFirst)
	if err != nil {
		t.Fatalf("parse %v: %v", argv, err)
	}
	return parsed
}

func TestColonCommandsParsePositionalArgs(t *testing.T) {
	cases := []struct {
		argv []string
		key  string
		want []string
	}{
		{[]string{"env:get", "FLYNN_POSTGRES"}, "<var>", []string{"FLYNN_POSTGRES"}},
		{[]string{"env", "get", "FLYNN_POSTGRES"}, "<var>", []string{"FLYNN_POSTGRES"}},
		{[]string{"env:set", "FOO=bar", "BAZ=qux"}, "<var>=<val>", []string{"FOO=bar", "BAZ=qux"}},
		{[]string{"env", "set", "FOO=bar"}, "<var>=<val>", []string{"FOO=bar"}},
		{[]string{"env:unset", "FOO", "BAR"}, "<var>", []string{"FOO", "BAR"}},
		{[]string{"env", "unset", "FOO"}, "<var>", []string{"FOO"}},
		{[]string{"meta:set", "a=1"}, "<var>=<val>", []string{"a=1"}},
		{[]string{"meta:unset", "a"}, "<var>", []string{"a"}},
		{[]string{"limit:set", "web", "memory=512MB"}, "<var>=<val>", []string{"memory=512MB"}},
		{[]string{"limit:runtime", "web", "small"}, "<profile>", []string{"small"}},
		{[]string{"limit", "runtime", "web", "medium"}, "<profile>", []string{"medium"}},
		{[]string{"ps:kill", "job-1", "job-2"}, "<job>", []string{"job-1", "job-2"}},
		{[]string{"kill", "job-1"}, "<job>", []string{"job-1"}},
		{[]string{"scale"}, "<type>=<spec>", nil},
		{[]string{"ps:scale", "web=1"}, "<type>=<spec>", []string{"web=1"}},
		{[]string{"pg:psql"}, "<argument>", nil},
		{[]string{"pg:psql", "--", "-c", "SELECT 1"}, "<argument>", []string{"-c", "SELECT 1"}},
		{[]string{"run", "bash"}, "<command>", []string{"bash"}},
		{[]string{"run", "bash", "-c", "true"}, "<argument>", []string{"-c", "true"}},
		{[]string{"resource:add", "redis"}, "<provider>", []string{"redis"}},
		{[]string{"resource:expose", "postgres"}, "<provider>", []string{"postgres"}},
		{[]string{"resource:unexpose", "mysql"}, "<provider>", []string{"mysql"}},
		{[]string{"route:remove", "http/abc"}, "<id>", []string{"http/abc"}},
		{[]string{"volume:show", "vol-1"}, "<id>", []string{"vol-1"}},
		{[]string{"cluster:add", "n", "d", "k"}, "<cluster-name>", []string{"n"}},
		{[]string{"log-sink:add", "syslog", "syslog://127.0.0.1:514"}, "<url>", []string{"syslog://127.0.0.1:514"}},
		{[]string{"log-sink", "add", "syslog", "syslog://127.0.0.1:514"}, "<url>", []string{"syslog://127.0.0.1:514"}},
		{[]string{"logsink:add", "syslog", "syslog://127.0.0.1:514"}, "<url>", []string{"syslog://127.0.0.1:514"}},
		{[]string{"log-sink:remove", "sink-1"}, "<id>", []string{"sink-1"}},
	}
	for _, tc := range cases {
		args := parseCLI(t, tc.argv)
		var got []string
		if tc.key == "<command>" {
			got = []string{cliutil.String(args, tc.key)}
		} else {
			got = cliutil.List(args, tc.key)
			if got == nil {
				got = []string{}
			}
		}
		want := tc.want
		if want == nil {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%v %s: got %q want %q", tc.argv, tc.key, got, want)
		}
	}
}

func TestEnvGetSingleVarIsStringInDocopt(t *testing.T) {
	args := parseCLI(t, []string{"env", "get", "FLYNN_POSTGRES"})
	if _, ok := args.All["<var>"].([]string); ok {
		t.Fatal("env:get <var> should be a string in All; List() must accept that")
	}
	if cliutil.String(args, "<var>") != "FLYNN_POSTGRES" {
		t.Fatalf("got %q", cliutil.String(args, "<var>"))
	}
}

func TestPluginListKnownParses(t *testing.T) {
	args := parseCLI(t, []string{"plugin:list"})
	if args.Bool["--known"] {
		t.Fatal("plugin:list must not imply --known")
	}
	args = parseCLI(t, []string{"plugin:list", "--known"})
	if !args.Bool["--known"] {
		t.Fatal("plugin:list --known")
	}
	args = parseCLI(t, []string{"plugin", "list", "--known"})
	if !args.Bool["--known"] {
		t.Fatal("plugin list --known")
	}
	args = parseCLI(t, []string{"plugin:list", "--check"})
	if !args.Bool["--check"] {
		t.Fatal("plugin:list --check")
	}
}

func TestRouteAddTCPTLSFlags(t *testing.T) {
	args := parseCLI(t, []string{"route:add", "tcp", "--service", "postgres", "--leader", "--domain", "postgres.example.com", "--tls-mode", "passthrough"})
	if !args.Bool["tcp"] || args.String["--service"] != "postgres" || !args.Bool["--leader"] {
		t.Fatalf("tcp flags: %+v %+v", args.Bool, args.String)
	}
	if args.String["--domain"] != "postgres.example.com" || args.String["--tls-mode"] != "passthrough" {
		t.Fatalf("tls flags: %+v", args.String)
	}
	expose := parseCLI(t, []string{"resource:expose", "redis", "--domain", "redis.example.com", "--auto-tls"})
	if expose.String["<provider>"] != "redis" || expose.String["--domain"] != "redis.example.com" || !expose.Bool["--auto-tls"] {
		t.Fatalf("resource expose: %+v %+v", expose.String, expose.Bool)
	}
}

func TestClusterRefreshParsesYes(t *testing.T) {
	args := parseCLI(t, []string{"cluster:refresh"})
	if args.Bool["--yes"] || args.Bool["--clear"] {
		t.Fatal("default refresh must not imply --yes or --clear")
	}
	args = parseCLI(t, []string{"cluster:refresh", "--yes"})
	if !args.Bool["--yes"] {
		t.Fatal("cluster:refresh --yes")
	}
	args = parseCLI(t, []string{"cluster:refresh", "-y"})
	if !args.Bool["--yes"] {
		t.Fatal("cluster:refresh -y")
	}
	args = parseCLI(t, []string{"cluster", "refresh", "--yes"})
	if !args.Bool["--yes"] {
		t.Fatal("cluster refresh --yes")
	}
	args = parseCLI(t, []string{"cluster:refresh", "--clear"})
	if !args.Bool["--clear"] || args.Bool["--yes"] {
		t.Fatal("cluster:refresh --clear must not imply --yes")
	}
}
