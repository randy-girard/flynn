package main

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/host/logmux"
	host "github.com/randy-girard/flynn/host/types"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
)

// stuckReader is a job log fd whose follow goroutine never returns: Close
// does not unblock Read until the test releases it.
type stuckReader struct {
	release chan struct{}
	once    sync.Once
}

func (r *stuckReader) Read([]byte) (int, error) {
	<-r.release
	return 0, io.EOF
}
func (r *stuckReader) Close() error { return nil }
func (r *stuckReader) unblock()     { r.once.Do(func() { close(r.release) }) }

func TestCleanupDoesNotBlockNewJobLogs(t *testing.T) {
	old := logStreamCloseTimeout
	logStreamCloseTimeout = 200 * time.Millisecond
	defer func() { logStreamCloseTimeout = old }()

	mux := logmux.New("host1", t.TempDir(), log15.New())
	l := &LibcontainerBackend{
		LibcontainerConfig: &LibcontainerConfig{Logger: log15.New(), LogMux: mux},
		logStreams:         make(map[string]map[string]*logmux.LogStream),
	}
	r := &stuckReader{release: make(chan struct{})}
	defer r.unblock()
	cfg := &logmux.Config{AppID: "flannel", HostID: "host1", JobID: "old-job"}
	l.logStreams["old-job"] = map[string]*logmux.LogStream{
		"stdout": mux.Follow(r, "", logagg.MsgIDStdout, cfg),
	}
	c := &Container{l: l, job: &host.Job{ID: "old-job"}}

	cleaned := make(chan struct{})
	go func() {
		c.closeLogStreams(l.Logger)
		close(cleaned)
	}()

	// A new job's followLogs takes logStreamMtx first. It must not wait for
	// the old job's stuck stream.
	locked := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		l.logStreamMtx.Lock()
		l.logStreamMtx.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("old job cleanup held logStreamMtx while its log stream would not close")
	}
	select {
	case <-cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup waited forever on a log stream that would not close")
	}
	l.logStreamMtx.Lock()
	_, ok := l.logStreams["old-job"]
	l.logStreamMtx.Unlock()
	if ok {
		t.Fatal("old job's log streams are still in the map")
	}
}
