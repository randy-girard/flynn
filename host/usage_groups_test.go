package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/cli"
)

func TestHostUsageCommandsAreAlphabetical(t *testing.T) {
	var names []string
	inCommands := false
	for _, line := range strings.Split(cli.RootHelp(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "See '") {
			break
		}
		if trimmed == "Commands:" {
			inCommands = true
			continue
		}
		if !strings.HasPrefix(line, "  ") && strings.HasSuffix(trimmed, ":") && trimmed != "Options:" && trimmed != "Commands:" {
			t.Fatalf("help must not group commands under %q", trimmed)
		}
		if !inCommands || trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	if len(names) < 2 {
		t.Fatal("expected command list")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("commands not alphabetical: %v", names)
	}
}
