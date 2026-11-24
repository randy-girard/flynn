package v1controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

// EDGE-75: an older worker set finished_at, then rolled back (up to 40 s),
// then wrote the failed event. A deployment with finished_at and status
// running is not a success.

// fakeRollbackController serves one deployment the way the old worker wrote
// it: pending, then finished_at with status still running while the rollback
// runs, then the failed event. The app's current release stays the old one.
type fakeRollbackController struct {
	mu         sync.Mutex
	status     string
	finishedAt *time.Time
	events     []ct.DeploymentEvent
	notify     chan struct{}
	appRelease string
}

const (
	fakeAppID   = "00000000-0000-0000-0000-0000000000a1"
	fakeOldRel  = "00000000-0000-0000-0000-0000000000b1"
	fakeNewRel  = "00000000-0000-0000-0000-0000000000b2"
	fakeDeploID = "00000000-0000-0000-0000-0000000000d1"
)

func newFakeRollbackController() *fakeRollbackController {
	return &fakeRollbackController{status: "pending", notify: make(chan struct{}, 16), appRelease: fakeOldRel}
}

func (f *fakeRollbackController) emit(e ct.DeploymentEvent, finished bool) {
	f.mu.Lock()
	if finished {
		now := time.Now()
		f.finishedAt = &now
	}
	if e.Status != "" {
		e.DeploymentID = fakeDeploID
		e.AppID = fakeAppID
		f.events = append(f.events, e)
		f.status = e.Status
	}
	f.mu.Unlock()
	f.notify <- struct{}{}
}

func (f *fakeRollbackController) deployment() *ct.Deployment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &ct.Deployment{ID: fakeDeploID, AppID: fakeAppID, OldReleaseID: fakeOldRel, NewReleaseID: fakeNewRel, Status: f.status, FinishedAt: f.finishedAt}
}

func (f *fakeRollbackController) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "POST" && r.URL.Path == "/apps/"+fakeAppID+"/deploy":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(f.deployment())
	case r.Method == "GET" && r.URL.Path == "/deployments/"+fakeDeploID:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(f.deployment())
	case r.Method == "GET" && r.URL.Path == "/apps/"+fakeAppID+"/release":
		f.mu.Lock()
		id := f.appRelease
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(&ct.Release{ID: id, AppID: fakeAppID})
	case r.Method == "GET" && r.URL.Path == "/events" && strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fl.Flush()
		sent := 0
		for {
			f.mu.Lock()
			pending := append([]ct.DeploymentEvent(nil), f.events[sent:]...)
			f.mu.Unlock()
			for _, e := range pending {
				sent++
				data, _ := json.Marshal(e)
				ev, _ := json.Marshal(&ct.Event{ID: int64(sent), AppID: fakeAppID, ObjectType: ct.EventTypeDeployment, ObjectID: fakeDeploID, Data: data})
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", sent, ev)
				fl.Flush()
			}
			select {
			case <-f.notify:
			case <-time.After(50 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
	case r.Method == "GET" && r.URL.Path == "/events":
		// deploymentFailedMessage lists past events.
		f.mu.Lock()
		var out []*ct.Event
		for i := len(f.events) - 1; i >= 0; i-- {
			data, _ := json.Marshal(f.events[i])
			out = append(out, &ct.Event{ID: int64(i + 1), ObjectType: ct.EventTypeDeployment, ObjectID: fakeDeploID, Data: data})
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	default:
		http.Error(w, "not found", 404)
	}
}

// The CORE-52 shape against a controller over HTTP: the deploy fails, the
// worker sets finished_at, rolls back for a while, then writes failed.
// DeployAppRelease must return the failure, not success.
func TestDeployAppReleaseReportsAFailureThatIsFinishedBeforeItsRollback(t *testing.T) {
	f := newFakeRollbackController()
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := &Client{Client: &httpclient.Client{URL: srv.URL, Key: "k", HTTP: srv.Client()}}

	go func() {
		time.Sleep(100 * time.Millisecond)
		f.emit(ct.DeploymentEvent{Status: "running", JobType: "web", JobState: ct.JobStateDown}, false)
		// The old worker's defer: finished_at first ...
		f.emit(ct.DeploymentEvent{}, true)
		// ... then the rollback (restore old, delete new) ...
		time.Sleep(3 * deployWaitPollInterval)
		// ... then the failed event.
		f.emit(ct.DeploymentEvent{Status: "failed", Error: "web job failed to start: exit 1"}, false)
	}()

	stop := make(chan struct{})
	timer := time.AfterFunc(20*time.Second, func() { close(stop) })
	defer timer.Stop()
	err := c.DeployAppRelease(fakeAppID, fakeNewRel, stop)
	if err == nil {
		t.Fatal("DeployAppRelease reported success for a deployment that ended failed (it took finished_at with status running as success)")
	}
	if !strings.Contains(err.Error(), "web job failed to start: exit 1") {
		t.Fatalf("err = %v, want the deployment's failure", err)
	}
}

// Same, at the wait loop: polls see finished_at with status running, then
// the stream delivers failed.
func TestWaitAppReleaseWaitsForTheOutcomeOfAFinishedDeploy(t *testing.T) {
	now := time.Now()
	events := make(chan *ct.DeploymentEvent, 1)
	time.AfterFunc(150*time.Millisecond, func() {
		events <- &ct.DeploymentEvent{Status: "failed", Error: "web job failed to start: exit 1"}
	})
	stop := make(chan struct{})
	timer := time.AfterFunc(5*time.Second, func() { close(stop) })
	defer timer.Stop()
	err := waitAppRelease(events, func() (*ct.Deployment, error) {
		return &ct.Deployment{ID: "d1", AppID: "a1", NewReleaseID: "new", Status: "running", FinishedAt: &now}, nil
	}, func() error {
		return errors.New("deployment failed")
	}, stop, 10*time.Millisecond, nil)
	if err == nil || err.Error() != "web job failed to start: exit 1" {
		t.Fatalf("err = %v, want the failed event's error (finished_at with status running is not a success)", err)
	}
}

// An older controller whose complete event was lost when the platform
// Postgres primary moved: finished_at, status running, and the app already
// runs the new release (the worker sets it only after Perform succeeded).
func TestDeployWaitFinishedOnTheNewReleaseSucceeds(t *testing.T) {
	now := time.Now()
	events := make(chan *ct.DeploymentEvent)
	close(events)
	stop := make(chan struct{})
	timer := time.AfterFunc(5*time.Second, func() { close(stop) })
	defer timer.Stop()
	err := deployWait{
		events: events,
		get: func() (*ct.Deployment, error) {
			return &ct.Deployment{ID: "d1", AppID: "a1", NewReleaseID: "new", Status: "running", FinishedAt: &now}, nil
		},
		isCurrent: func(d *ct.Deployment) (bool, error) { return d.NewReleaseID == "new", nil },
		stopWait:  stop,
		interval:  10 * time.Millisecond,
		grace:     time.Hour,
	}.run()
	if err != nil {
		t.Fatal(err)
	}
}

// finished_at, status running, the stream gives nothing more and the app is
// still on its old release: after the grace the wait fails and says why.
func TestDeployWaitFinishedWithoutAnOutcomeFails(t *testing.T) {
	now := time.Now()
	events := make(chan *ct.DeploymentEvent)
	close(events)
	stop := make(chan struct{})
	timer := time.AfterFunc(5*time.Second, func() { close(stop) })
	defer timer.Stop()
	start := time.Now()
	err := deployWait{
		events: events,
		get: func() (*ct.Deployment, error) {
			return &ct.Deployment{ID: "d1", AppID: "a1", NewReleaseID: "new", Status: "running", FinishedAt: &now}, nil
		},
		isCurrent: func(*ct.Deployment) (bool, error) { return false, nil },
		stopWait:  stop,
		interval:  10 * time.Millisecond,
		grace:     100 * time.Millisecond,
	}.run()
	if err == nil || !strings.Contains(err.Error(), "has finished_at but no complete or failed status") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("failed before the grace was over")
	}
}

// A current controller writes a rollback marker (status running, Progress
// "rolling back: ...") before the rollback: it is progress, not an outcome.
func TestDeployWaitLogsTheRollbackAndWaitsForFailed(t *testing.T) {
	events := make(chan *ct.DeploymentEvent, 2)
	events <- &ct.DeploymentEvent{Status: "running", RollingBack: true, Progress: "rolling back: web job failed to start: exit 1"}
	time.AfterFunc(50*time.Millisecond, func() {
		events <- &ct.DeploymentEvent{Status: "failed", Error: "web job failed to start: exit 1"}
	})
	stop := make(chan struct{})
	var progress []string
	err := deployWait{
		events: events,
		get: func() (*ct.Deployment, error) {
			return &ct.Deployment{ID: "d1", Status: "running"}, nil
		},
		stopWait:   stop,
		interval:   10 * time.Millisecond,
		grace:      time.Hour,
		onProgress: func(e *ct.DeploymentEvent) { progress = append(progress, e.Progress) },
	}.run()
	if err == nil || err.Error() != "web job failed to start: exit 1" {
		t.Fatalf("err = %v", err)
	}
	if len(progress) != 1 || !strings.HasPrefix(progress[0], "rolling back: ") {
		t.Fatalf("progress = %v", progress)
	}
}
