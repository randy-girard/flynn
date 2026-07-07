package fixer

import (
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
	state "github.com/flynn/flynn/pkg/sirenia/state"
)

func TestInstanceWithJobID(t *testing.T) {
	instances := []*discoverd.Instance{
		{Meta: map[string]string{"FLYNN_JOB_ID": "job-a"}},
		{Meta: map[string]string{"FLYNN_JOB_ID": "job-b"}},
		{Meta: nil},
	}
	if !instanceWithJobID(instances, "job-b") {
		t.Fatal("expected job-b to be found")
	}
	if instanceWithJobID(instances, "job-c") {
		t.Fatal("did not expect job-c to be found")
	}
	if instanceWithJobID(nil, "job-a") {
		t.Fatal("no instances should never match")
	}
}

func TestSireniaDiscoverdStateStale(t *testing.T) {
	f := &ClusterFixer{}
	const release = "rel-1"

	// No primary => stale.
	if !f.sireniaDiscoverdStateStale(&state.State{}, nil, release) {
		t.Fatal("empty state should be stale")
	}
	if !f.sireniaDiscoverdStateStale(nil, nil, release) {
		t.Fatal("nil state should be stale")
	}

	// Primary from a different release => stale.
	stale := &state.State{Primary: &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": "old-rel"}}}
	if !f.sireniaDiscoverdStateStale(stale, nil, release) {
		t.Fatal("primary on different release should be stale")
	}

	// Primary job not present among instances => stale.
	staleJob := &state.State{Primary: &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": release, "FLYNN_JOB_ID": "gone"}}}
	if !f.sireniaDiscoverdStateStale(staleJob, nil, release) {
		t.Fatal("primary job missing from instances should be stale")
	}

	// Healthy: matching release and job present.
	instances := []*discoverd.Instance{{Meta: map[string]string{"FLYNN_JOB_ID": "here"}}}
	healthy := &state.State{Primary: &discoverd.Instance{Meta: map[string]string{"FLYNN_RELEASE_ID": release, "FLYNN_JOB_ID": "here"}}}
	if f.sireniaDiscoverdStateStale(healthy, instances, release) {
		t.Fatal("matching release + present job should not be stale")
	}
}
