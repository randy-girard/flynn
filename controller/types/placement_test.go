package types

import "testing"

func TestParseHostMaxProcesses(t *testing.T) {
	if _, ok := ParseHostMaxProcesses(nil); ok {
		t.Fatal("nil tags have no cap")
	}
	if _, ok := ParseHostMaxProcesses(map[string]string{"role": "db"}); ok {
		t.Fatal("missing key has no cap")
	}
	for _, raw := range []string{"", "unlimited", "off", "none", "0", "-1", "x"} {
		if _, ok := ParseHostMaxProcesses(map[string]string{FormationMaxProcessesTag: raw}); ok {
			t.Fatalf("%q must not be a cap", raw)
		}
	}
	n, ok := ParseHostMaxProcesses(map[string]string{FormationMaxProcessesTag: "1"})
	if !ok || n != 1 {
		t.Fatalf("got %d ok=%t", n, ok)
	}
	if EncodeHostMaxProcesses(1) != "1" || EncodeHostMaxProcesses(0) != "" {
		t.Fatal("encode")
	}
}

func TestParseHostPersistentSingletons(t *testing.T) {
	if _, ok := ParseHostPersistentSingletons(nil); ok {
		t.Fatal("nil tags have no cap")
	}
	if _, ok := ParseHostPersistentSingletons(map[string]string{FormationMaxProcessesTag: "4"}); ok {
		t.Fatal("the process cap is not the singleton cap")
	}
	for _, raw := range []string{"", "unlimited", "off", "none", "auto", "0", "-1", "lots"} {
		if _, ok := ParseHostPersistentSingletons(map[string]string{FormationMaxPersistentSingletonsTag: raw}); ok {
			t.Fatalf("%q must not be a cap", raw)
		}
	}
	n, ok := ParseHostPersistentSingletons(map[string]string{FormationMaxPersistentSingletonsTag: " 2 "})
	if !ok || n != 2 {
		t.Fatalf("got %d ok=%t", n, ok)
	}
}

func TestMergePlacementTagsRuntimeWins(t *testing.T) {
	got := MergePlacementTags(
		map[string]string{"role": "web", "disk": "ssd"},
		map[string]string{"role": "database"},
	)
	if got["role"] != "database" || got["disk"] != "ssd" {
		t.Fatalf("%v", got)
	}
	if MergePlacementTags(nil, nil) != nil {
		t.Fatal("empty merge")
	}
}

func TestFormationTagsChanged(t *testing.T) {
	if FormationTagsChanged(nil, nil) || FormationTagsChanged(map[string]map[string]string{}, nil) {
		t.Fatal("empty")
	}
	old := map[string]map[string]string{"web": {"role": "web"}}
	if !FormationTagsChanged(old, nil) {
		t.Fatal("cleared tags")
	}
	if FormationTagsChanged(old, map[string]map[string]string{"web": {"role": "web"}}) {
		t.Fatal("same")
	}
	if !ProcessesChanged(map[string]int{"web": 1}, map[string]int{"web": 2}) {
		t.Fatal("count change")
	}
	if ProcessesChanged(map[string]int{"web": 1}, map[string]int{"web": 1}) {
		t.Fatal("same count")
	}
}
