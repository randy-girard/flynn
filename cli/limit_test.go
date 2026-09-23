package main

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
)

func TestLookupRuntimeProfileByNameAndID(t *testing.T) {
	list := []*ct.RuntimeProfile{
		{ID: "abc", Name: "small", Memory: 512 * 1024 * 1024, CPU: 500, Builtin: true},
		{ID: "def", Name: "xlarge", Memory: 1, CPU: 2},
		nil,
	}
	if p := lookupRuntimeProfile(list, "SMALL"); p == nil || p.Name != "small" {
		t.Fatalf("name lookup: %+v", p)
	}
	if p := lookupRuntimeProfile(list, "def"); p == nil || p.Name != "xlarge" {
		t.Fatalf("id lookup: %+v", p)
	}
	if p := lookupRuntimeProfile(list, "missing"); p != nil {
		t.Fatalf("missing: %+v", p)
	}
}

func TestApplyRuntimeProfileToProcSetsLimits(t *testing.T) {
	p := &ct.RuntimeProfile{Name: "small", Memory: 512 * 1024 * 1024, CPU: 500}
	got := applyRuntimeProfileToProc(ct.ProcessType{}, p)
	if got.RuntimeProfile != "small" {
		t.Fatalf("profile %q", got.RuntimeProfile)
	}
	if got.Resources[resource.TypeMemory].Limit == nil || *got.Resources[resource.TypeMemory].Limit != p.Memory {
		t.Fatalf("memory %+v", got.Resources[resource.TypeMemory])
	}
	if got.Resources[resource.TypeCPU].Limit == nil || *got.Resources[resource.TypeCPU].Limit != p.CPU {
		t.Fatalf("cpu %+v", got.Resources[resource.TypeCPU])
	}
}

func TestFormatRuntimeProfiles(t *testing.T) {
	out := formatRuntimeProfiles([]*ct.RuntimeProfile{
		{Name: "large", Memory: 2 * 1024 * 1024 * 1024, CPU: 2000, Builtin: true},
		{Name: "small", Memory: 512 * 1024 * 1024, CPU: 500, Builtin: true},
	})
	if !strings.Contains(out, "small") || !strings.Contains(out, "large") {
		t.Fatalf("output %q", out)
	}
	smallAt := strings.Index(out, "small")
	largeAt := strings.Index(out, "large")
	if smallAt < 0 || largeAt < 0 || largeAt > smallAt {
		t.Fatalf("expected name sort, got %q", out)
	}
}

func TestResolveCommandLimitProfiles(t *testing.T) {
	name, args, from := resolveCommand("limit", []string{"profiles"})
	if name != "limit:profiles" || from != "limit profiles" || len(args) != 0 {
		t.Fatalf("got %q %q from=%q", name, args, from)
	}
	name, args, from = resolveCommand("limit", []string{"profile", "web", "small"})
	if name != "limit:profile" || from != "limit profile" || strings.Join(args, " ") != "web small" {
		t.Fatalf("apply got %q %q from=%q", name, args, from)
	}
	name, args, from = resolveCommand("limit", []string{"runtime", "web", "small"})
	if name != "limit:runtime" || from != "limit runtime" || strings.Join(args, " ") != "web small" {
		t.Fatalf("runtime got %q %q from=%q", name, args, from)
	}
}
