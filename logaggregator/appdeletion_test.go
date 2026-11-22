package main

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	. "github.com/flynn/go-check"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/stream"
)

// fakeEventSource plays one batch of events per StreamEvents call, then
// ends that stream, so the watcher has to reconnect.
type fakeEventSource struct {
	mu      sync.Mutex
	batches [][]*ct.Event
	opts    []ct.StreamEventsOptions
	calls   int
}

type fakeStream struct{}

func (f *fakeStream) Close() error { return nil }
func (f *fakeStream) Err() error   { return nil }

var _ stream.Stream = (*fakeStream)(nil)

func (f *fakeEventSource) StreamEvents(opts ct.StreamEventsOptions, ch chan *ct.Event) (stream.Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.opts = append(f.opts, opts)
	if len(f.batches) == 0 {
		return nil, errors.New("controller unavailable")
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	go func() {
		for _, e := range batch {
			ch <- e
		}
		close(ch)
	}()
	return &fakeStream{}, nil
}

func deletionEvent(appID, errMsg string) *ct.Event {
	data, _ := json.Marshal(ct.AppDeletionEvent{AppDeletion: &ct.AppDeletion{AppID: appID}, Error: errMsg})
	return &ct.Event{AppID: appID, ObjectID: appID, ObjectType: ct.EventTypeAppDeletion, Data: data}
}

func (s *LogAggregatorTestSuite) TestAppDeletionWatcherDropsBuffers(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()
	for _, id := range []string{"app-1", "app-2", "app-3", "app-keep"} {
		aggr.feed(newMessageForApp(id, "web.1", "line"))
	}

	src := &fakeEventSource{batches: [][]*ct.Event{
		// catch-up of past deletions, then one live deletion
		{deletionEvent("app-1", ""), deletionEvent("app-2", "")},
		// a failed deletion leaves the app (and its logs) in place
		{deletionEvent("app-keep", "route delete failed"), deletionEvent("app-3", "")},
	}}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watchAppDeletions(aggr, src, stop, time.Millisecond)
		close(done)
	}()

	waitFor(c, func() bool { return aggr.BufferCount() == 1 })
	c.Assert(aggr.Read("app-keep"), HasLen, 1)
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		c.Fatal("watcher did not stop")
	}

	src.mu.Lock()
	defer src.mu.Unlock()
	c.Assert(src.calls >= 2, Equals, true)
	for _, o := range src.opts {
		c.Assert(o.ObjectTypes, DeepEquals, []ct.EventType{ct.EventTypeAppDeletion})
		c.Assert(o.AppID, Equals, "")
		// every (re)connect replays recent deletions so none missed
		// while disconnected (or before this instance started) leak
		c.Assert(o.Past, Equals, true)
		c.Assert(o.Count > 0, Equals, true)
	}
}

func (s *LogAggregatorTestSuite) TestServerWatchesAppDeletions(c *C) {
	src := &fakeEventSource{batches: [][]*ct.Event{{deletionEvent("srv-deleted", "")}}}
	srv := NewServer(ServerConfig{
		SyslogAddr: "127.0.0.1:0",
		ApiAddr:    "127.0.0.1:0",
		APIKeys:    []string{testAPIKey},
		AppEvents:  src,
		BufferTTL:  time.Hour,
	})
	c.Assert(srv.Aggregator.ttl, Equals, time.Hour)
	srv.Aggregator.feed(newMessageForApp("srv-deleted", "web.1", "x"))
	srv.Aggregator.feed(newMessageForApp("srv-kept", "web.1", "y"))
	c.Assert(srv.Start(), IsNil)
	waitFor(c, func() bool { return srv.Aggregator.BufferCount() == 1 })
	c.Assert(srv.Aggregator.Read("srv-kept"), HasLen, 1)

	done := make(chan struct{})
	go func() { srv.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		c.Fatal("shutdown hung")
	}
}

func (s *LogAggregatorTestSuite) TestParseBufferTTL(c *C) {
	c.Assert(parseBufferTTL(""), Equals, DefaultBufferTTL)
	c.Assert(parseBufferTTL("72h"), Equals, 72*time.Hour)
	c.Assert(parseBufferTTL("nonsense"), Equals, DefaultBufferTTL)
	c.Assert(parseBufferTTL("-1h"), Equals, DefaultBufferTTL)
	c.Assert(parseBufferTTL("0s"), Equals, DefaultBufferTTL)
}
