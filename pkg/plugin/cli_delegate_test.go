package plugin

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestMatchFlynnDelegate(t *testing.T) {
	c := &CLI{
		Command: "widget",
		Actions: []CLIAction{
			{Name: "route", Flynn: "route"},
			{Name: "env", Flynn: "env"},
		},
	}
	action, rest, ok := c.MatchFlynnDelegate([]string{"route", "add", "http", "--auto-tls", "widget.example"})
	if !ok || action == nil || action.Flynn != "route" {
		t.Fatalf("action=%+v ok=%v", action, ok)
	}
	if len(rest) != 4 || rest[0] != "add" || rest[3] != "widget.example" {
		t.Fatalf("rest=%q", rest)
	}
	if _, _, ok := c.MatchFlynnDelegate([]string{"unknown"}); ok {
		t.Fatal("unknown subcommand")
	}
	if _, _, ok := c.MatchFlynnDelegate(nil); ok {
		t.Fatal("empty argv")
	}
	nameless := &CLI{Actions: []CLIAction{{Flynn: "route"}}}
	action, rest, ok = nameless.MatchFlynnDelegate([]string{"route"})
	if !ok || action.Flynn != "route" || len(rest) != 0 {
		t.Fatalf("name defaults to flynn command: %+v %q", action, rest)
	}
}

func TestManifestValidateCLIFlynn(t *testing.T) {
	ok := &Manifest{
		Name: "widget",
		Kind: KindApp,
		App:  AppSpec{Processes: map[string]ct.ProcessType{"web": {}}},
		CLI: &CLI{
			Command: "widget",
			Actions: []CLIAction{{Name: "route", Flynn: "route"}},
		},
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	if ok.CLI.Actions[0].Name != "route" {
		t.Fatal(ok.CLI.Actions[0].Name)
	}

	both := *ok
	both.CLI = &CLI{Command: "widget", Actions: []CLIAction{{Flynn: "route", Args: []string{"/bin/x"}}}}
	if err := both.Validate(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("args+flynn: %v", err)
	}

	spaces := *ok
	spaces.CLI = &CLI{Command: "widget", Actions: []CLIAction{{Flynn: "route add"}}}
	if err := spaces.Validate(); err == nil || !strings.Contains(err.Error(), "single command") {
		t.Fatalf("spaces: %v", err)
	}

	fill := *ok
	fill.CLI = &CLI{Command: "widget", Actions: []CLIAction{{Flynn: "route"}}}
	if err := fill.Validate(); err != nil {
		t.Fatal(err)
	}
	if fill.CLI.Actions[0].Name != "route" {
		t.Fatalf("default name=%q", fill.CLI.Actions[0].Name)
	}
}
