package updaterdeploy

import (
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
)

func TestRegisteredSireniaJobs(t *testing.T) {
	instances := []*discoverd.Instance{
		{Meta: map[string]string{"FLYNN_JOB_ID": "node1-abc"}},
		{Meta: map[string]string{"FLYNN_JOB_ID": "node2-def"}},
		nil,
		{Meta: map[string]string{}},
	}
	got := registeredSireniaJobs(instances)
	if len(got) != 2 {
		t.Fatalf("expected 2 registered jobs, got %d", len(got))
	}
	if !got["node1-abc"] || !got["node2-def"] {
		t.Fatalf("unexpected registered set: %#v", got)
	}
}

func TestSireniaInstanceForJob(t *testing.T) {
	want := &discoverd.Instance{
		Addr: "100.100.1.1:3306",
		Meta: map[string]string{"FLYNN_JOB_ID": "node3-ghi"},
	}
	instances := []*discoverd.Instance{
		{Meta: map[string]string{"FLYNN_JOB_ID": "other"}},
		want,
	}
	if got := sireniaInstanceForJob(instances, "node3-ghi"); got != want {
		t.Fatalf("sireniaInstanceForJob: got %v want %v", got, want)
	}
	if got := sireniaInstanceForJob(instances, "missing"); got != nil {
		t.Fatalf("expected nil for missing job, got %v", got)
	}
}
