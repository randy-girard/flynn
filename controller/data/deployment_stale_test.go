package data

import (
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestOpenDeployIsStale(t *testing.T) {
	now := time.Now()
	created := now.Add(-3 * time.Minute)
	open := &ct.Deployment{
		ID:            "d1",
		Status:        "running",
		DeployTimeout: 120,
		CreatedAt:     &created,
	}
	if openDeployIsStale(open, deployQueJob{Exists: true, Locked: true}, now) {
		t.Fatal("a locked worker must keep isolate_deploys")
	}
	if !openDeployIsStale(open, deployQueJob{}, now) {
		t.Fatal("no que job means the deployer is gone and the lock is stale")
	}
	if !openDeployIsStale(&ct.Deployment{ID: "d2", Status: "complete"}, deployQueJob{Exists: true}, now) {
		t.Fatal("complete without finished_at must be released")
	}
	if !openDeployIsStale(&ct.Deployment{ID: "d3", Status: "failed"}, deployQueJob{}, now) {
		t.Fatal("failed without finished_at must be released")
	}
	recent := now.Add(-5 * time.Second)
	pending := &ct.Deployment{ID: "d4", Status: "pending", DeployTimeout: 120, CreatedAt: &recent}
	if openDeployIsStale(pending, deployQueJob{Exists: true, Locked: false}, now) {
		t.Fatal("a queued job that has not timed out is still in progress")
	}
	if !openDeployIsStale(open, deployQueJob{Exists: true, Locked: false}, now) {
		t.Fatal("an unlocked job older than deploy_timeout is not actually rolling")
	}
	if openDeployIsStale(&ct.Deployment{ID: "d5", FinishedAt: &now, Status: "running"}, deployQueJob{}, now) {
		t.Fatal("finished_at already set is not stale")
	}
}

func TestIsDeployInProgress(t *testing.T) {
	if !ct.IsDeployInProgress(ct.ValidationError{Message: ct.DeployInProgressMessage}) {
		t.Fatal("isolate_deploys validation must match")
	}
	if ct.IsDeployInProgress(nil) || ct.IsDeployInProgress(ct.ValidationError{Message: "other"}) {
		t.Fatal("unrelated errors must not match")
	}
}
