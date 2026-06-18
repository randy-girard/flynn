package deployment

import (
	"testing"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/sirenia/state"
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
		t.Fatal("expected cluster without asyncs to be not ready")
	}

	singleton := &state.State{Singleton: true}
	if !sireniaClusterDeployReady(singleton, 1) {
		t.Fatal("expected singleton cluster to be deploy-ready")
	}
}
