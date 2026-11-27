package cli

import (
	"strings"
	"testing"
)

// NEW-46: flynn-host user:list printed the `user` help, because the shared
// noun:list -> noun rewrite ran before the registered user:list was looked
// up. Every registered X:list must run as itself, from main (RewriteHelpList)
// and from ResolveCommand.
func TestRegisteredListCommandsAreNotRewritten(t *testing.T) {
	var n int
	for name := range commands {
		if !strings.HasSuffix(name, ":list") {
			continue
		}
		n++
		if got, _ := RewriteHelpList(name, nil); got != name {
			t.Errorf("RewriteHelpList(%q) = %q, want the registered command", name, got)
		}
		if got, _, _ := ResolveCommand(name, nil); got != name {
			t.Errorf("ResolveCommand(%q) = %q, want the registered command", name, got)
		}
	}
	if n == 0 {
		t.Fatal("no registered :list commands found")
	}
}

func TestUserListRunsList(t *testing.T) {
	name, args, _ := ResolveCommand("user:list", []string{"--json"})
	if name != "user:list" || len(args) != 1 || args[0] != "--json" {
		t.Fatalf("user:list resolved to %q %v, want user:list [--json]", name, args)
	}
	// unregistered noun:list and noun:help still rewrite
	if got, _ := RewriteHelpList("volume:list", nil); commands["volume:list"] == nil && got != "volume" {
		t.Fatalf("unregistered volume:list rewrote to %q, want volume", got)
	}
	if got, args := RewriteHelpList("user:help", nil); got != "user" || len(args) != 1 || args[0] != "--help" {
		t.Fatalf("user:help rewrote to %q %v, want user --help", got, args)
	}
}
