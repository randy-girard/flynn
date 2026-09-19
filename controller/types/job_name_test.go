package types

import (
	"testing"
)

func TestAllocateJobNameUniqueAndFormat(t *testing.T) {
	used := []string{"web.1", "web.2"}
	seen := map[string]bool{"web.1": true, "web.2": true}
	for i := 0; i < 20; i++ {
		name := AllocateJobName("web", used)
		if !IsJobName(name) {
			t.Fatalf("invalid name %q", name)
		}
		if seen[name] {
			t.Fatalf("reused %q", name)
		}
		seen[name] = true
		used = append(used, name)
	}
}

func TestIsJobName(t *testing.T) {
	for _, ok := range []string{"web.1", "worker.4821", "runner.99", "new-web.12"} {
		if !IsJobName(ok) {
			t.Fatalf("want match %q", ok)
		}
	}
	for _, bad := range []string{"", "web", "web.0", "web.10000", "WEB.01", "host0-uuid"} {
		if IsJobName(bad) {
			t.Fatalf("want reject %q", bad)
		}
	}
}

func TestEnsureAndDisplayName(t *testing.T) {
	j := &Job{Type: "worker", UUID: "abcd1234-0000-0000-0000-000000000000"}
	if got := JobDisplayName(j); len(got) < 8 || got[:7] != "worker." {
		t.Fatalf("synthetic %q", got)
	}
	j.Name = "worker.42"
	EnsureJobName(j)
	if j.Meta["name"] != "worker.42" {
		t.Fatalf("meta %v", j.Meta)
	}
	j2 := &Job{Type: "web", Meta: map[string]string{"name": "web.7"}}
	EnsureJobName(j2)
	if j2.Name != "web.7" {
		t.Fatalf("name %q", j2.Name)
	}
}
