package main

import (
	"reflect"
	"testing"

	"github.com/flynn/flynn/pkg/cliutil"
	"github.com/flynn/go-docopt"
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
		{[]string{"ps:kill", "job-1", "job-2"}, "<job>", []string{"job-1", "job-2"}},
		{[]string{"kill", "job-1"}, "<job>", []string{"job-1"}},
		{[]string{"scale"}, "<type>=<spec>", nil},
		{[]string{"ps:scale", "web=1"}, "<type>=<spec>", []string{"web=1"}},
		{[]string{"pg:psql"}, "<argument>", nil},
		{[]string{"pg:psql", "--", "-c", "SELECT 1"}, "<argument>", []string{"-c", "SELECT 1"}},
		{[]string{"run", "bash"}, "<command>", []string{"bash"}},
		{[]string{"run", "bash", "-c", "true"}, "<argument>", []string{"-c", "true"}},
		{[]string{"resource:add", "redis"}, "<provider>", []string{"redis"}},
		{[]string{"route:remove", "http/abc"}, "<id>", []string{"http/abc"}},
		{[]string{"volume:show", "vol-1"}, "<id>", []string{"vol-1"}},
		{[]string{"cluster:add", "n", "d", "k"}, "<cluster-name>", []string{"n"}},
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
