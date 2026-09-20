package deployment

import (
	"testing"

	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func TestSireniaClusterDeployReady(t *testing.T) {
	peer := func(n int) *discoverd.Instance {
		return &discoverd.Instance{Meta: map[string]string{"ID": "p"}}
	}
	_ = peer

	threeNode := &state.State{
		Primary: &discoverd.Instance{},
		Sync:    &discoverd.Instance{},
		Async:   []*discoverd.Instance{{}},
	}
	if !sireniaClusterDeployReady(threeNode, 3) {
		t.Fatal("expected 3-node cluster with one async to be deploy-ready")
	}

	noAsync := &state.State{
		Primary: &discoverd.Instance{},
		Sync:    &discoverd.Instance{},
		Deposed: []*discoverd.Instance{{}},
	}
	if sireniaClusterDeployReady(noAsync, 3) {
		t.Fatal("expected cluster without asyncs to be not deploy-ready")
	}

	haMissingAsync := &state.State{
		Primary: &discoverd.Instance{},
		Sync:    &discoverd.Instance{},
	}
	if sireniaClusterDeployReady(haMissingAsync, 3) {
		t.Fatal("expected HA cluster without asyncs to be not deploy-ready")
	}

	singleton := &state.State{Singleton: true}
	if !sireniaClusterDeployReady(singleton, 1) {
		t.Fatal("expected singleton cluster to be deploy-ready")
	}
}

func TestFindSireniaPeerByRelease(t *testing.T) {
	old := &discoverd.Instance{
		ID: "old",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-old",
			"FLYNN_PROCESS_TYPE": "postgres",
		},
	}
	first := &discoverd.Instance{
		ID: "new-1",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-new",
			"FLYNN_PROCESS_TYPE": "postgres",
		},
	}
	second := &discoverd.Instance{
		ID: "new-2",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-new",
			"FLYNN_PROCESS_TYPE": "postgres",
		},
	}
	insts := []*discoverd.Instance{old, first, second}

	got := findSireniaPeerByRelease(insts, "rel-new", "postgres", "POSTGRES_ID")
	if got != first {
		t.Fatalf("want first new peer, got %#v", got)
	}
	got = findSireniaPeerByRelease(insts, "rel-new", "postgres", "POSTGRES_ID", first)
	if got != second {
		t.Fatalf("want second new peer after excluding first, got %#v", got)
	}
	got = findSireniaPeerByRelease(insts, "rel-new", "postgres", "POSTGRES_ID", first, second)
	if got != nil {
		t.Fatalf("want nil when all new peers excluded, got %#v", got)
	}
	if sireniaPeerMatchesRelease(nil, "rel-new", "postgres") {
		t.Fatal("nil instance must not match")
	}

	rereg := &discoverd.Instance{
		ID: "new-1-reregistered",
		Meta: map[string]string{
			"FLYNN_RELEASE_ID":   "rel-new",
			"FLYNN_PROCESS_TYPE": "postgres",
			"POSTGRES_ID":        "peer-1",
		},
	}
	first.Meta["POSTGRES_ID"] = "peer-1"
	second.Meta["POSTGRES_ID"] = "peer-2"
	got = findSireniaPeerByRelease([]*discoverd.Instance{old, rereg, second}, "rel-new", "postgres", "POSTGRES_ID", first)
	if got != second {
		t.Fatalf("want second peer when first re-registered with a new discoverd ID, got %#v", got)
	}
}
