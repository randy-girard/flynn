package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/stream"
)

// silentFormationController accepts every formation stream and then never
// sends anything, not even the empty "current" marker. The real controller
// does this when its database query hangs: the HTTP response is open and the
// sse writer keeps sending ":" keepalives, so the client never sees an error
// or a disconnect.
type silentFormationController struct {
	*testutils.FakeControllerClient
	connects int32
}

type silentStream struct {
	once sync.Once
	ch   chan<- *ct.ExpandedFormation
}

func (s *silentStream) Close() error {
	s.once.Do(func() { close(s.ch) })
	return nil
}

func (s *silentStream) Err() error { return nil }

func (c *silentFormationController) StreamFormations(since *time.Time, ch chan<- *ct.ExpandedFormation) (stream.Stream, error) {
	atomic.AddInt32(&c.connects, 1)
	return &silentStream{ch: ch}, nil
}

// A formation stream that connects but never delivers the current marker used
// to leave the scheduler waiting forever (observed for over an hour on
// dev-node2: "still waiting for current formation list" with no reconnect).
// The scheduler must drop such a stream, reconnect, and when the snapshot
// still does not arrive within the overall bound, exit so discoverd drops it
// and another scheduler can lead.
func TestStreamFormationEventsReconnectsAndExitsWhenSnapshotNeverArrives(t *testing.T) {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	cc := &silentFormationController{FakeControllerClient: testutils.NewFakeControllerClient()}
	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), l)
	s.formationSnapshotTimeout = 40 * time.Millisecond
	s.formationSnapshotGiveUp = 400 * time.Millisecond
	exited := make(chan struct{})
	var once sync.Once
	s.exit = func() { once.Do(func() { close(exited) }) }

	returned := make(chan struct{})
	go func() {
		s.streamFormationEvents()
		close(returned)
	}()

	select {
	case <-exited:
	case <-returned:
		t.Fatal("streamFormationEvents returned without a current formation list")
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler kept waiting for the formation list and never exited")
	}
	if n := atomic.LoadInt32(&cc.connects); n < 2 {
		t.Fatalf("connected %d time(s), want a reconnect after the snapshot deadline", n)
	}
}

// A scheduler stopped while it waits for the formation list must stop
// waiting, not exit the process when the give-up timer fires. A test
// scheduler stopped before its snapshot used to end the whole test binary
// five minutes later, so `go test -count=N` died after ~5m with no message.
func TestStoppedSchedulerDoesNotExitWaitingForFormations(t *testing.T) {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	cc := &silentFormationController{FakeControllerClient: testutils.NewFakeControllerClient()}
	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), l)
	s.formationSnapshotTimeout = 40 * time.Millisecond
	s.formationSnapshotGiveUp = 300 * time.Millisecond
	var exits int32
	s.exit = func() { atomic.AddInt32(&exits, 1) }

	returned := make(chan struct{})
	go func() {
		s.streamFormationEvents()
		close(returned)
	}()
	time.Sleep(50 * time.Millisecond)
	s.Stop()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("streamFormationEvents kept waiting after Stop")
	}
	time.Sleep(400 * time.Millisecond)
	if n := atomic.LoadInt32(&exits); n != 0 {
		t.Fatalf("stopped scheduler called exit %d time(s)", n)
	}
}

func TestFormationSnapshotBoundsDefaults(t *testing.T) {
	s := NewScheduler(nil, nil, nil, log15.New())
	if s.formationSnapshotTimeout <= 0 || s.formationSnapshotGiveUp <= s.formationSnapshotTimeout {
		t.Fatalf("bad defaults: timeout=%s giveUp=%s", s.formationSnapshotTimeout, s.formationSnapshotGiveUp)
	}
	if s.exit == nil {
		t.Fatal("exit hook not set")
	}
}
