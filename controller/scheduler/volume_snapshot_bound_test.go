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

// silentVolumeController accepts every volume stream and never sends the
// empty "current" marker, like a controller whose volume list query hangs.
type silentVolumeController struct {
	*testutils.FakeControllerClient
	connects int32
}

type silentVolumeStream struct {
	once sync.Once
	ch   chan *ct.Volume
}

func (s *silentVolumeStream) Close() error {
	s.once.Do(func() { close(s.ch) })
	return nil
}

func (s *silentVolumeStream) Err() error { return nil }

func (c *silentVolumeController) StreamVolumes(since *time.Time, ch chan *ct.Volume) (stream.Stream, error) {
	atomic.AddInt32(&c.connects, 1)
	return &silentVolumeStream{ch: ch}, nil
}

// NEW-09: Run() waits for the volume list after it registered in discoverd.
// A volume stream that connects but never delivers the current marker used to
// keep the scheduler registered and waiting forever. It must reconnect after
// the per-connection deadline and exit once the overall bound passes.
func TestStreamVolumeEventsReconnectsAndExitsWhenSnapshotNeverArrives(t *testing.T) {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	cc := &silentVolumeController{FakeControllerClient: testutils.NewFakeControllerClient()}
	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), l)
	s.snapshotTimeout = 40 * time.Millisecond
	s.snapshotGiveUp = 400 * time.Millisecond
	exited := make(chan struct{})
	var once sync.Once
	s.exit = func() { once.Do(func() { close(exited) }) }

	returned := make(chan struct{})
	go func() {
		s.streamVolumeEvents()
		close(returned)
	}()

	select {
	case <-exited:
	case <-returned:
		t.Fatal("streamVolumeEvents returned without a current volume list")
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler kept waiting for the volume list and never exited")
	}
	if n := atomic.LoadInt32(&cc.connects); n < 2 {
		t.Fatalf("connected %d time(s), want a reconnect after the snapshot deadline", n)
	}
}

// A scheduler stopped while it waits for the volume list stops waiting and
// does not exit the process when the give-up timer fires.
func TestStoppedSchedulerDoesNotExitWaitingForVolumes(t *testing.T) {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	cc := &silentVolumeController{FakeControllerClient: testutils.NewFakeControllerClient()}
	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), l)
	s.snapshotTimeout = 40 * time.Millisecond
	s.snapshotGiveUp = 300 * time.Millisecond
	var exits int32
	s.exit = func() { atomic.AddInt32(&exits, 1) }

	returned := make(chan struct{})
	go func() {
		s.streamVolumeEvents()
		close(returned)
	}()
	time.Sleep(50 * time.Millisecond)
	s.Stop()

	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("streamVolumeEvents kept waiting after Stop")
	}
	time.Sleep(400 * time.Millisecond)
	if n := atomic.LoadInt32(&exits); n != 0 {
		t.Fatalf("stopped scheduler called exit %d time(s)", n)
	}
}

// Volumes that arrive before the marker are applied directly, and the stream
// resumes from the last volume's UpdatedAt after a reconnect.
func TestStreamVolumeEventsAppliesSnapshotAndResumesSince(t *testing.T) {
	l := log15.New()
	l.SetHandler(log15.DiscardHandler())
	updated := time.Now().Add(-time.Minute)
	cc := &scriptedVolumeController{
		FakeControllerClient: testutils.NewFakeControllerClient(),
		first:                []*ct.Volume{{ID: "vol-1", HostID: "host-1", UpdatedAt: &updated}},
	}
	s := NewScheduler(newTestCluster(nil), cc, newFakeDiscoverd(true), l)
	s.snapshotTimeout = 40 * time.Millisecond
	s.snapshotGiveUp = 2 * time.Second
	s.exit = func() { t.Error("exit called although the snapshot arrived") }

	done := make(chan struct{})
	go func() {
		s.streamVolumeEvents()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("streamVolumeEvents did not return after the second connection sent the marker")
	}
	if _, ok := s.volumes["vol-1"]; !ok {
		t.Fatal("volume from the first connection was not applied")
	}
	cc.mtx.Lock()
	defer cc.mtx.Unlock()
	if len(cc.sinces) < 2 || cc.sinces[1] == nil || !cc.sinces[1].Equal(updated) {
		t.Fatalf("reconnect since = %v, want %s", cc.sinces, updated)
	}
}

// scriptedVolumeController sends one volume and no marker on the first
// connection, then only the marker on later ones.
type scriptedVolumeController struct {
	*testutils.FakeControllerClient
	first  []*ct.Volume
	mtx    sync.Mutex
	sinces []*time.Time
}

func (c *scriptedVolumeController) StreamVolumes(since *time.Time, ch chan *ct.Volume) (stream.Stream, error) {
	c.mtx.Lock()
	c.sinces = append(c.sinces, since)
	n := len(c.sinces)
	c.mtx.Unlock()
	if n == 1 {
		for _, v := range c.first {
			ch <- v
		}
	} else {
		ch <- &ct.Volume{}
	}
	return &silentVolumeStream{ch: ch}, nil
}
