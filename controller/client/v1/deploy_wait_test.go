package v1controller

import (
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestDeploymentFinishedTreatsRunningWithFinishedAtAsComplete(t *testing.T) {
	now := time.Now()
	if !deploymentFinished(&ct.Deployment{Status: "running", FinishedAt: &now}) {
		t.Fatal("postgres HA deploys emit job events with status=running; after finished_at is set the complete event can be lost during failover")
	}
	if !deploymentFinished(&ct.Deployment{Status: "complete"}) {
		t.Fatal("status=complete must be finished")
	}
	if deploymentFinished(&ct.Deployment{Status: "failed", FinishedAt: &now}) {
		t.Fatal("failed must not be treated as success")
	}
	if deploymentFinished(&ct.Deployment{Status: "pending"}) {
		t.Fatal("in-flight deploy is not finished")
	}
	if deploymentFinished(&ct.Deployment{Status: "running"}) {
		t.Fatal("running without finished_at is still in progress")
	}
	if deploymentFinished(nil) {
		t.Fatal("nil deployment is not finished")
	}
}

func TestWaitAppReleaseUnblocksWhenSSEStallsAfterPostgresRestart(t *testing.T) {
	// flynn-host update logs "waiting for deployment to complete" then blocks
	// in DeployAppRelease. A sirenia postgres deploy kills the primary; the
	// controller SSE connection stays open but never delivers "complete".
	// The worker still sets finished_at (and the last event is often
	// status=running from a job up/down). Without polling, the updater hangs
	// until the 30m stopWait.
	now := time.Now()
	events := make(chan *ct.DeploymentEvent)
	go func() {
		events <- &ct.DeploymentEvent{Status: "running", JobType: "postgres", JobState: ct.JobStateUp}
	}()

	stop := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		errCh <- waitAppRelease(events, func() (*ct.Deployment, error) {
			return &ct.Deployment{Status: "running", FinishedAt: &now}, nil
		}, func() error {
			return errors.New("deployment failed")
		}, stop, 15*time.Millisecond)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("stalled SSE after postgres restart must not hang: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		close(stop)
		t.Fatal("DeployAppRelease hung on a live SSE stream after postgres finished_at was set")
	}
}

func TestWaitAppReleaseCompleteEvent(t *testing.T) {
	events := make(chan *ct.DeploymentEvent, 1)
	events <- &ct.DeploymentEvent{Status: "complete"}
	stop := make(chan struct{})
	if err := waitAppRelease(events, func() (*ct.Deployment, error) {
		t.Fatal("complete event should not need a status poll")
		return nil, nil
	}, nil, stop, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestWaitAppReleaseFailedEvent(t *testing.T) {
	events := make(chan *ct.DeploymentEvent, 1)
	events <- &ct.DeploymentEvent{Status: "failed", Error: "sirenia cluster in unhealthy state"}
	stop := make(chan struct{})
	err := waitAppRelease(events, func() (*ct.Deployment, error) {
		return &ct.Deployment{Status: "pending"}, nil
	}, nil, stop, time.Hour)
	if err == nil || err.Error() != "sirenia cluster in unhealthy state" {
		t.Fatalf("got %v", err)
	}
}

func TestWaitAppReleasePollsCompleteWhileStreamOpen(t *testing.T) {
	events := make(chan *ct.DeploymentEvent)
	var n int32
	stop := make(chan struct{})
	err := waitAppRelease(events, func() (*ct.Deployment, error) {
		if atomic.AddInt32(&n, 1) < 2 {
			return &ct.Deployment{Status: "pending"}, nil
		}
		return &ct.Deployment{Status: "complete"}, nil
	}, nil, stop, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&n) < 2 {
		t.Fatalf("expected polling until complete, polls=%d", n)
	}
}

func TestWaitAppReleaseCancelled(t *testing.T) {
	events := make(chan *ct.DeploymentEvent)
	stop := make(chan struct{})
	time.AfterFunc(20*time.Millisecond, func() { close(stop) })
	err := waitAppRelease(events, func() (*ct.Deployment, error) {
		return &ct.Deployment{Status: "pending"}, nil
	}, nil, stop, time.Hour)
	if err == nil || err.Error() != "deploy wait cancelled" {
		t.Fatalf("got %v", err)
	}
}

func TestDeployAppReleasePollsWhileStreamOpen(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	fn := strings.Index(body, "func (c *Client) DeployAppRelease")
	if fn < 0 {
		t.Fatal("DeployAppRelease missing")
	}
	wait := strings.Index(body, "func waitAppRelease")
	if wait < 0 || wait < fn {
		t.Fatal("DeployAppRelease must poll via waitAppRelease so a stalled postgres SSE stream cannot hang flynn-host update")
	}
	if !strings.Contains(body[fn:wait], "waitAppRelease(") {
		t.Fatal("DeployAppRelease must call waitAppRelease")
	}
}

func TestWaitAppReleaseKeepsPollingAfterStreamEnds(t *testing.T) {
	events := make(chan *ct.DeploymentEvent)
	close(events)
	var n int32
	stop := make(chan struct{})
	err := waitAppRelease(events, func() (*ct.Deployment, error) {
		if atomic.AddInt32(&n, 1) < 2 {
			return &ct.Deployment{Status: "running"}, nil
		}
		now := time.Now()
		return &ct.Deployment{Status: "running", FinishedAt: &now}, nil
	}, nil, stop, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeployAppReleaseRetriesIsolateDeploys(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "ct.IsDeployInProgress(err)") {
		t.Fatal("DeployAppRelease must retry isolate_deploys so resource:remove is not stuck behind a finished lock")
	}
	if !strings.Contains(body, "time.After(deployWaitPollInterval)") {
		t.Fatal("isolate_deploys retry must poll so a leftover row can be released")
	}
}
