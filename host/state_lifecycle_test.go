package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/host/logmux"
	host "github.com/randy-girard/flynn/host/types"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

// Regression for a host-wide hang seen mid-upgrade: AddJob held s.mtx while
// sendEvent wrote the lifecycle log line through the log mux, and a per-app
// follower that had stopped reading blocked that write forever, so every
// /host/jobs read on the host timed out. Lifecycle I/O now runs on a worker
// off the lock; AddJob and GetJob must return promptly even while a follower
// is stalled, and the line must still reach a healthy follower.
func TestAddJobDoesNotBlockOnStalledLogFollower(t *testing.T) {
	mux := logmux.New("host1", t.TempDir(), log15.New())
	// Block the writer: the mux waits its broadcast timeout (1s) on this
	// subscriber for every line. The state lock must not be held meanwhile.
	stalled := make(chan *rfc5424.Message)
	stalledStream, err := mux.StreamLog("app-1", "", false, true, stalled)
	if err != nil {
		t.Fatal(err)
	}
	defer stalledStream.Close()
	healthy := make(chan *rfc5424.Message, 16)
	healthyStream, err := mux.StreamLog("app-1", "", false, true, healthy)
	if err != nil {
		t.Fatal(err)
	}
	defer healthyStream.Close()
	// Let both followers subscribe before the writes.
	time.Sleep(50 * time.Millisecond)

	state := NewState("host1", filepath.Join(t.TempDir(), "host-state-db"))
	if err := state.OpenDB(); err != nil {
		t.Fatal(err)
	}
	defer state.CloseDB()
	state.logMux = mux

	job := func(id string) *host.Job {
		return &host.Job{ID: id, Config: host.ContainerConfig{Env: map[string]string{
			"FLYNN_APP_ID":       "app-1",
			"FLYNN_PROCESS_TYPE": "web",
		}}}
	}

	start := time.Now()
	for _, id := range []string{"host1-a", "host1-b", "host1-c"} {
		if err := state.AddJob(job(id)); err != nil {
			t.Fatal(err)
		}
		if state.GetJob(id) == nil {
			t.Fatalf("job %s missing after AddJob", id)
		}
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("AddJob/GetJob x3 took %s with a stalled follower; the state lock is being held across lifecycle I/O", took)
	}

	// The worker is still delivering (1s per line per stalled follower); the
	// healthy follower must eventually see all three creates, in order.
	deadline := time.After(10 * time.Second)
	for _, want := range []string{"host1-a", "host1-b", "host1-c"} {
		select {
		case msg := <-healthy:
			if string(msg.MsgID) != string(logagg.MsgIDSystem) {
				t.Fatalf("msgid=%s", msg.MsgID)
			}
			if got := string(msg.ProcID); got != "web."+want {
				t.Fatalf("procid=%q want %q (lifecycle lines must stay ordered)", got, "web."+want)
			}
		case <-deadline:
			t.Fatalf("healthy follower never received the lifecycle line for %s", want)
		}
	}
	if n := state.LifecycleDropped(); n != 0 {
		t.Fatalf("dropped %d lifecycle events with an idle queue", n)
	}
}

func TestLifecycleQueueDropsWhenFullInsteadOfBlocking(t *testing.T) {
	s := &State{lifecycle: make(chan lifecycleEvent)} // unbuffered, no worker started by the test
	s.lifecycleOnce.Do(func() {})                     // prevent the worker so the channel never drains
	j := &host.ActiveJob{Job: &host.Job{ID: "j"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			s.enqueueLifecycle(j, host.JobEventCreate)
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueueLifecycle blocked on a full queue")
	}
	if got := s.LifecycleDropped(); got != 10 {
		t.Fatalf("dropped=%d want 10", got)
	}
}
