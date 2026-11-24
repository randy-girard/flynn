package fixer

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

// EDGE-68, 20:23:59 on 2026-11-23: after a node reboot the flynn-host
// monitor's cluster repair (FixController) read the system Postgres current
// release e0165d85 at postgres=0 and wrote postgres=3, while release
// 0a8a5e46 already ran all three engines. Six engines ran for about 40
// minutes. A formation that is low only because another release runs the
// engines is not broken.
func TestFixFormationWantSkipsSireniaEnginesOnAnotherRelease(t *testing.T) {
	current := &ct.Formation{ReleaseID: "e0165d85", Processes: map[string]int{"postgres": 0, "web": 2}}
	formations := []*ct.Formation{
		{ReleaseID: "0a8a5e46", Processes: map[string]int{"postgres": 3}},
		current,
	}
	if want, other := fixFormationWant(current, formations, "postgres", true, 3); want != 0 || other != "0a8a5e46" {
		t.Fatalf("want=%d other=%q; the current release must not be scaled up while 0a8a5e46 runs the engines", want, other)
	}
}

func TestFixFormationWantRestoresABrokenSireniaFormation(t *testing.T) {
	current := &ct.Formation{ReleaseID: "e0165d85", Processes: map[string]int{"postgres": 0, "web": 2}}
	formations := []*ct.Formation{
		{ReleaseID: "0a8a5e46", Processes: map[string]int{"postgres": 0}},
		current,
	}
	if want, _ := fixFormationWant(current, formations, "postgres", true, 3); want != 3 {
		t.Fatalf("want=%d, want 3: no formation runs any engine", want)
	}
	if want, _ := fixFormationWant(current, formations, "postgres", true, 1); want != 1 {
		t.Fatalf("single host: want=%d, want 1", want)
	}
	// already at its count
	full := &ct.Formation{ReleaseID: "x", Processes: map[string]int{"postgres": 3}}
	if want, _ := fixFormationWant(full, []*ct.Formation{full}, "postgres", true, 3); want != 0 {
		t.Fatalf("want=%d for a full formation", want)
	}
}

// Non-sirenia types keep the old rule: at least one process on the current
// release, whatever other releases run.
func TestFixFormationWantNonSireniaType(t *testing.T) {
	current := &ct.Formation{ReleaseID: "b", Processes: map[string]int{"web": 0}}
	formations := []*ct.Formation{{ReleaseID: "a", Processes: map[string]int{"web": 2}}, current}
	if want, _ := fixFormationWant(current, formations, "web", false, 1); want != 1 {
		t.Fatalf("want=%d, want 1", want)
	}
}
