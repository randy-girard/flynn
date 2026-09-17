package main

import (
	"os"
	"sort"
	"strings"
	"testing"
)

func TestHostUsageCommandsAreAlphabetical(t *testing.T) {
	body, err := os.ReadFile("host.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "Commands:\n")
	end := strings.Index(src, "See 'flynn-host help")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("could not find flynn-host usage command list")
	}
	var names []string
	for _, line := range strings.Split(src[start:end], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, ":") && trimmed != "Commands:" {
			t.Fatalf("help must not group commands under %q", trimmed)
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				names = append(names, fields[0])
			}
		}
	}
	if len(names) < 2 {
		t.Fatal("expected command list")
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("commands not alphabetical: %v", names)
	}
}
