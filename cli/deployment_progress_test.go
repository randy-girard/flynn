package main

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestInProgressDeployment(t *testing.T) {
	done := time.Now()
	got := inProgressDeployment([]*ct.Deployment{
		{ID: "complete", Status: "complete", FinishedAt: &done},
		{ID: "running", Status: "running"},
		{ID: "pending", Status: "pending"},
	})
	if got == nil || got.ID != "running" {
		t.Fatalf("want running deploy first, got %#v", got)
	}
	if inProgressDeployment([]*ct.Deployment{{ID: "done", Status: "complete", FinishedAt: &done}}) != nil {
		t.Fatal("finished deploys must not be in progress")
	}
	if inProgressDeployment([]*ct.Deployment{{ID: "cfg", Status: "running", Type: ct.ReleaseTypeConfig}}) != nil {
		t.Fatal("resource-attach config deploys must not count as in-progress")
	}
}
