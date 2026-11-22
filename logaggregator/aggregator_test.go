package main

import (
	"bytes"
	"fmt"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/logaggregator/snapshot"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func (s *LogAggregatorTestSuite) TestAggregator(c *C) {
	data := zip(appAMessages[:100], appCRunMessages, appBJob2Messages, appCWebMessages, appBJob1Messages)

	aggr := NewAggregator()
	defer aggr.Shutdown()

	for _, msg := range data {
		aggr.feed(msg)
	}

	tests := []struct {
		id   string
		want []*rfc5424.Message
	}{
		{
			id:   "app-A",
			want: appAMessages[:100],
		},
		{
			id:   "app-B",
			want: zip(appBJob2Messages, appBJob1Messages),
		},
		{
			id:   "app-C",
			want: zip(appCRunMessages, appCWebMessages),
		},
	}

	for _, test := range tests {
		got := aggr.Read(test.id)

		c.Assert(len(got), Equals, len(test.want))
		c.Assert(got, DeepEquals, test.want)
	}
}

func msgAt(app string, ts time.Time, line string) *rfc5424.Message {
	m := newMessageForApp(app, "web.1", line)
	m.Timestamp = ts
	return m
}

func (s *LogAggregatorTestSuite) TestReadOfUnknownAppDoesNotAllocate(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()

	for i := 0; i < 100; i++ {
		c.Assert(aggr.Read(fmt.Sprintf("never-logged-%d", i)), HasLen, 0)
	}
	c.Assert(aggr.BufferCount(), Equals, 0)

	// The API read path (no follow) must not allocate either.
	for i := 0; i < 10; i++ {
		logrc, err := s.client.GetLog(fmt.Sprintf("api-unknown-%d", i), &logagg.LogOpts{Lines: typeconv.IntPtr(10)})
		c.Assert(err, IsNil)
		assertAllLogsEquals(c, logrc, "")
		logrc.Close()
	}
	c.Assert(s.agg.BufferCount(), Equals, 0)
}

func (s *LogAggregatorTestSuite) TestBufferForDeletedAppIsDropped(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()

	aggr.feed(newMessageForApp("deleted-app", "web.1", "goodbye"))
	aggr.feed(newMessageForApp("live-app", "web.1", "hello"))
	c.Assert(aggr.BufferCount(), Equals, 2)

	// a follower of the deleted app is ended, not left hanging
	msgc := make(chan *rfc5424.Message, 10)
	donec := make(chan struct{})
	defer close(donec)
	aggr.Subscribe("deleted-app", msgc, donec)

	aggr.DropApp("deleted-app")
	c.Assert(aggr.BufferCount(), Equals, 1)
	c.Assert(aggr.Read("deleted-app"), HasLen, 0)
	c.Assert(aggr.Read("live-app"), HasLen, 1)
	select {
	case _, ok := <-msgc:
		c.Assert(ok, Equals, false)
	case <-time.After(5 * time.Second):
		c.Fatal("follower of deleted app was not closed")
	}

	// late lines from the deleted app's dying jobs do not recreate it
	aggr.feed(newMessageForApp("deleted-app", "web.1", "late line"))
	c.Assert(aggr.Read("deleted-app"), HasLen, 0)
	c.Assert(aggr.BufferCount(), Equals, 1)

	// and the snapshot a new instance loads does not carry it
	var buf bytes.Buffer
	c.Assert(snapshot.WriteTo(aggr.ReadAll(), &buf), IsNil)
	sc := snapshot.NewScanner(&buf)
	for sc.Scan() {
		c.Assert(string(sc.Message.AppName), Equals, "live-app")
	}
	c.Assert(sc.Err(), IsNil)
}

func (s *LogAggregatorTestSuite) TestIdleBufferExpires(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()
	aggr.ttl = time.Hour

	t0 := time.Now().Add(-10 * time.Hour)
	aggr.feed(msgAt("idle-app", t0, "old"))
	aggr.feed(msgAt("busy-app", t0, "old"))
	aggr.feed(msgAt("busy-app", t0.Add(9*time.Hour+30*time.Minute), "recent"))

	// a subscribed (followed) buffer is kept even when idle
	msgc := make(chan *rfc5424.Message, 10)
	donec := make(chan struct{})
	aggr.feed(msgAt("followed-app", t0, "old"))
	aggr.Subscribe("followed-app", msgc, donec)

	aggr.expire(time.Now())
	c.Assert(aggr.Read("idle-app"), HasLen, 0)
	c.Assert(aggr.Read("busy-app"), HasLen, 2)
	c.Assert(aggr.Read("followed-app"), HasLen, 1)
	c.Assert(aggr.BufferCount(), Equals, 2)

	// once the follower leaves, the idle buffer goes on the next sweep
	close(donec)
	for range msgc {
	}
	waitFor(c, func() bool { aggr.expire(time.Now()); return aggr.BufferCount() == 1 })
	c.Assert(aggr.Read("followed-app"), HasLen, 0)

	// the expired app's history is gone but it can log again
	aggr.feed(newMessageForApp("idle-app", "web.1", "back"))
	c.Assert(aggr.Read("idle-app"), HasLen, 1)
}

func (s *LogAggregatorTestSuite) TestFollowOfUnknownAppStillReceivesLines(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()

	msgc := make(chan *rfc5424.Message, 10)
	donec := make(chan struct{})
	aggr.Subscribe("new-app", msgc, donec)

	// an empty followed buffer survives a sweep
	aggr.expire(time.Now())
	aggr.feed(newMessageForApp("new-app", "web.1", "first line"))
	select {
	case m := <-msgc:
		c.Assert(string(m.Msg), Equals, "first line")
	case <-time.After(5 * time.Second):
		c.Fatal("follower did not get the first line")
	}

	// an empty buffer nobody follows is dropped on the next sweep
	aggr.Subscribe("nobody-logs", make(chan *rfc5424.Message, 1), donec)
	close(donec)
	waitFor(c, func() bool { aggr.expire(time.Now()); return aggr.BufferCount() == 1 })
}

func (s *LogAggregatorTestSuite) TestSnapshotSkipsExpiredAndEmptyBuffers(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()
	aggr.ttl = time.Hour

	aggr.feed(msgAt("stale-app", time.Now().Add(-2*time.Hour), "stale"))
	aggr.feed(newMessageForApp("fresh-app", "web.1", "fresh"))
	donec := make(chan struct{})
	defer close(donec)
	aggr.Subscribe("empty-app", make(chan *rfc5424.Message, 1), donec)

	got := aggr.ReadAll()
	c.Assert(got, HasLen, 1)
	c.Assert(got[0], HasLen, 1)
	c.Assert(string(got[0][0].AppName), Equals, "fresh-app")
}

func (s *LogAggregatorTestSuite) TestDeletedAppTombstoneExpires(c *C) {
	aggr := NewAggregator()
	defer aggr.Shutdown()
	aggr.ttl = time.Hour

	aggr.DropApp("gone")
	aggr.feed(newMessageForApp("gone", "web.1", "late"))
	c.Assert(aggr.Read("gone"), HasLen, 0)
	c.Assert(aggr.TombstoneCount(), Equals, 1)

	aggr.expire(time.Now().Add(2 * time.Hour))
	c.Assert(aggr.TombstoneCount(), Equals, 0)
}

func waitFor(c *C, cond func() bool) {
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.Fatal("condition not met")
}
