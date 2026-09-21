package main

import (
	"testing"

	"github.com/randy-girard/flynn/host/resource"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func TestJobPidsLimitDefault(t *testing.T) {
	if got := jobPidsLimit(nil); got != resource.DefaultPidsLimit {
		t.Fatalf("nil job = %d, want %d", got, resource.DefaultPidsLimit)
	}
	if got := jobPidsLimit(&host.Job{}); got != resource.DefaultPidsLimit {
		t.Fatalf("empty job = %d, want %d", got, resource.DefaultPidsLimit)
	}
	if got := jobPidsLimit(&host.Job{Resources: resource.Resources{}}); got != resource.DefaultPidsLimit {
		t.Fatalf("empty resources = %d, want %d", got, resource.DefaultPidsLimit)
	}
}

func TestJobPidsLimitOverride(t *testing.T) {
	job := &host.Job{Resources: resource.Resources{
		resource.TypeMaxProcs: resource.Spec{Limit: typeconv.Int64Ptr(128)},
	}}
	if got := jobPidsLimit(job); got != 128 {
		t.Fatalf("override = %d, want 128", got)
	}
}

func TestJobPidsLimitNonPositiveUsesDefault(t *testing.T) {
	for _, n := range []int64{0, -1} {
		job := &host.Job{Resources: resource.Resources{
			resource.TypeMaxProcs: resource.Spec{Limit: typeconv.Int64Ptr(n)},
		}}
		if got := jobPidsLimit(job); got != resource.DefaultPidsLimit {
			t.Fatalf("max_procs=%d => %d, want default %d", n, got, resource.DefaultPidsLimit)
		}
	}
}

func TestJobPidsLimitAppliesToSystemAndBuild(t *testing.T) {
	system := &host.Job{Partition: "system"}
	if got := jobPidsLimit(system); got != resource.DefaultPidsLimit {
		t.Fatalf("system job = %d, want default", got)
	}
	build := &host.Job{Metadata: map[string]string{"flynn-controller.type": "dockerbuilder"}}
	if got := jobPidsLimit(build); got != resource.DefaultPidsLimit {
		t.Fatalf("build job = %d, want default", got)
	}
}
