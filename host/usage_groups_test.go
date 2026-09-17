package main

import (
	"os"
	"sort"
	"strings"
	"testing"
)

func TestHostUsageGroupsAreAlphabetical(t *testing.T) {
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
	var group []string
	check := func() {
		t.Helper()
		if !sort.StringsAreSorted(group) {
			t.Fatalf("group not alphabetical: %v", group)
		}
		group = nil
	}
	for _, line := range strings.Split(src[start:end], "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				group = append(group, fields[0])
			}
			continue
		}
		if strings.HasSuffix(trimmed, ":") {
			check()
		}
	}
	check()
}
