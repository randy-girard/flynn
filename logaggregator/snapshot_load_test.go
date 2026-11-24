package main

import (
	"bytes"
	"encoding/gob"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/logaggregator/snapshot"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

// EDGE-31: a starting instance reads the running instances' snapshot. A
// source that stalls, drops the connection mid-record or sends less than it
// said must not hold startup, must not lose what was read, and must not
// leave host cursors that make hosts skip lines the instance never got.

const snapApp = "snap-app"

func snapMessages(n int) []*rfc5424.Message {
	msgs := make([]*rfc5424.Message, n)
	for i := range msgs {
		m := newMessageForApp(snapApp, "web.1", "line "+strconv.Itoa(i))
		m.Hostname = []byte("host-a")
		m.Timestamp = time.Now().Add(time.Duration(i-n) * time.Second)
		msgs[i] = m
	}
	return msgs
}

// gobRecords encodes msgs the way a snapshot does and returns the bytes and
// the offset at which each record ends.
func gobRecords(c *C, msgs []*rfc5424.Message) ([]byte, []int) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	ends := make([]int, len(msgs))
	for i, m := range msgs {
		c.Assert(enc.Encode(m), IsNil)
		ends[i] = buf.Len()
	}
	return buf.Bytes(), ends
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// stallingSource sends the first n records of msgs, then sends nothing more
// until the request is cancelled or release is closed.
func stallingSource(c *C, msgs []*rfc5424.Message, n int, release chan struct{}) *httptest.Server {
	b, ends := gobRecords(c, msgs)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(b[:ends[n-1]])
		flush(w)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
}

// cutSource sends the first n records of msgs and half of the next one,
// then drops the connection.
func cutSource(c *C, msgs []*rfc5424.Message, n int) *httptest.Server {
	b, ends := gobRecords(c, msgs)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(b[:ends[n-1]+(ends[n]-ends[n-1])/2])
		flush(w)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
}

// countSource sends msgs and a count trailer of count.
func countSource(c *C, msgs []*rfc5424.Message, count int) *httptest.Server {
	b, _ := gobRecords(c, msgs)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", snapshotCountTrailer)
		w.Write(b)
		w.Header().Set(snapshotCountTrailer, strconv.Itoa(count))
	}))
}

// legacySource is a leader from before EDGE-31: the snapshot, no count.
func legacySource(msgs []*rfc5424.Message) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot.WriteTo([][]*rfc5424.Message{msgs}, w)
	}))
}

// peerSource is a running instance with msgs buffered, serving its real API.
func peerSource(c *C, msgs []*rfc5424.Message) (*httptest.Server, *Server) {
	peer := testServer(c)
	for _, m := range msgs {
		peer.Aggregator.feed(m)
	}
	return httptest.NewServer(peer.api), peer
}

func closedSource(c *C) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, IsNil)
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func testLoader(sources ...string) snapshotLoader {
	return snapshotLoader{
		Sources:       sources,
		Key:           testAPIKey,
		HTTP:          snapshotHTTPClient(time.Second),
		IdleTimeout:   300 * time.Millisecond,
		SourceTimeout: 5 * time.Second,
		TotalTimeout:  10 * time.Second,
	}
}

// loadWithin runs the load and fails the test if it does not return in
// bound.
func (s *LogAggregatorTestSuite) loadWithin(c *C, bound time.Duration, l snapshotLoader) snapshotResult {
	done := make(chan snapshotResult, 1)
	start := time.Now()
	go func() { done <- s.srv.loadSnapshotFromPeers(l) }()
	select {
	case res := <-done:
		c.Logf("load returned %+v in %s", res, time.Since(start))
		return res
	case <-time.After(bound):
		c.Fatalf("snapshot load still running after %s", bound)
	}
	return snapshotResult{}
}

func (s *LogAggregatorTestSuite) waitForLines(c *C, n int) {
	waitFor(c, func() bool { return len(s.srv.Aggregator.Read(snapApp)) == n })
	time.Sleep(50 * time.Millisecond)
	c.Assert(s.srv.Aggregator.Read(snapApp), HasLen, n)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadFromStalledLeaderTimesOut(c *C) {
	msgs := snapMessages(5)
	release := make(chan struct{})
	src := stallingSource(c, msgs, 3, release)
	defer src.Close()
	defer src.CloseClientConnections()
	defer close(release)

	res := s.loadWithin(c, 3*time.Second, testLoader(src.URL))
	c.Assert(res.Complete, Equals, false)
	c.Assert(res.Messages, Equals, 3)
	// the three lines read are kept, but no cursor: hosts resend the rest
	s.waitForLines(c, 3)
	c.Assert(s.srv.Cursors.Get(), HasLen, 0)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadEOFMidRecordKeepsLinesNotCursors(c *C) {
	src := cutSource(c, snapMessages(5), 3)
	defer src.Close()

	res := s.loadWithin(c, 3*time.Second, testLoader(src.URL))
	c.Assert(res.Complete, Equals, false)
	c.Assert(res.Messages, Equals, 3)
	s.waitForLines(c, 3)
	c.Assert(s.srv.Cursors.Get(), HasLen, 0)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadFallsBackToAnotherInstance(c *C) {
	msgs := snapMessages(5)
	cut := cutSource(c, msgs, 3)
	defer cut.Close()
	peer, peerSrv := peerSource(c, msgs)
	defer peer.Close()
	defer peerSrv.Shutdown()

	res := s.loadWithin(c, 3*time.Second, testLoader(cut.URL, peer.URL))
	c.Assert(res.Complete, Equals, true)
	c.Assert(res.Source, Equals, peer.URL)
	// the lines read from the cut leader and the peer are not doubled
	s.waitForLines(c, 5)
	cursors := s.srv.Cursors.Get()
	c.Assert(cursors, HasLen, 1)
	last, err := utils.ParseHostCursor(msgs[4])
	c.Assert(err, IsNil)
	c.Assert(cursors["host-a"].Seq, Equals, last.Seq)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadShortOfItsCountIsIncomplete(c *C) {
	src := countSource(c, snapMessages(3), 6)
	defer src.Close()

	res := s.loadWithin(c, 3*time.Second, testLoader(src.URL))
	c.Assert(res.Complete, Equals, false)
	c.Assert(res.Messages, Equals, 3)
	s.waitForLines(c, 3)
	c.Assert(s.srv.Cursors.Get(), HasLen, 0)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadWithCountIsComplete(c *C) {
	src := countSource(c, snapMessages(3), 3)
	defer src.Close()

	res := s.loadWithin(c, 3*time.Second, testLoader(src.URL))
	c.Assert(res.Complete, Equals, true)
	s.waitForLines(c, 3)
	c.Assert(s.srv.Cursors.Get(), HasLen, 1)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadFromOlderLeaderWithoutCount(c *C) {
	src := legacySource(snapMessages(4))
	defer src.Close()

	res := s.loadWithin(c, 3*time.Second, testLoader(src.URL))
	c.Assert(res.Complete, Equals, true)
	c.Assert(res.Messages, Equals, 4)
	s.waitForLines(c, 4)
	c.Assert(s.srv.Cursors.Get(), HasLen, 1)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadWithNoReachableSourceIsBounded(c *C) {
	release := make(chan struct{})
	stall := stallingSource(c, snapMessages(2), 1, release)
	defer stall.Close()
	defer stall.CloseClientConnections()
	defer close(release)

	res := s.loadWithin(c, 3*time.Second, testLoader(closedSource(c), stall.URL))
	c.Assert(res.Complete, Equals, false)
	c.Assert(s.srv.Cursors.Get(), HasLen, 0)
	// the instance still starts
	c.Assert(s.srv.Start(), IsNil)
}

func (s *LogAggregatorTestSuite) TestSnapshotLoadTotalTimeoutBoundsSlowSources(c *C) {
	// each source trickles a record before the idle timeout, so only the
	// per-source and total bounds stop it
	msgs := snapMessages(200)
	b, ends := gobRecords(c, msgs)
	trickle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prev := 0
		for _, e := range ends {
			if _, err := w.Write(b[prev:e]); err != nil {
				return
			}
			flush(w)
			prev = e
			select {
			case <-r.Context().Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}))
	defer trickle.Close()
	defer trickle.CloseClientConnections()

	l := testLoader(trickle.URL, trickle.URL, trickle.URL)
	l.IdleTimeout = time.Second
	l.SourceTimeout = 700 * time.Millisecond
	l.TotalTimeout = 1500 * time.Millisecond
	start := time.Now()
	res := s.loadWithin(c, 4*time.Second, l)
	c.Assert(res.Complete, Equals, false)
	c.Assert(time.Since(start) < 2500*time.Millisecond, Equals, true)
	c.Assert(s.srv.Cursors.Get(), HasLen, 0)
}

func (s *LogAggregatorTestSuite) TestSnapshotHandlerSendsCount(c *C) {
	for _, m := range snapMessages(2) {
		s.agg.feed(m)
	}
	req, err := http.NewRequest("GET", s.api.URL+"/snapshot", nil)
	c.Assert(err, IsNil)
	req.SetBasicAuth("", testAPIKey)
	res, err := http.DefaultClient.Do(req)
	c.Assert(err, IsNil)
	defer res.Body.Close()
	c.Assert(res.StatusCode, Equals, 200)
	_, err = io.Copy(io.Discard, res.Body)
	c.Assert(err, IsNil)
	c.Assert(res.Trailer.Get(snapshotCountTrailer), Equals, "2")
}

// A stopping instance finishes the snapshots new instances already asked it
// for (a deploy stops the old jobs as soon as the new ones are up).
func (s *LogAggregatorTestSuite) TestShutdownFinishesSnapshotInFlight(c *C) {
	srv := testServer(c)
	for _, m := range snapMessages(3) {
		srv.Aggregator.feed(m)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	srv.snapshots.hook = func() { close(entered); <-release }
	c.Assert(srv.Start(), IsNil)

	l := testLoader(testClient(c, srv).URL)
	l.IdleTimeout = 5 * time.Second
	done := make(chan snapshotResult, 1)
	go func() { done <- s.srv.loadSnapshotFromPeers(l) }()
	<-entered
	released := false
	defer func() {
		if !released {
			close(release)
			<-done
		}
	}()

	shut := make(chan struct{})
	go func() { srv.Shutdown(); close(shut) }()
	select {
	case <-shut:
		c.Fatal("Shutdown returned while a snapshot was still being written")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	released = true
	select {
	case <-shut:
	case <-time.After(3 * time.Second):
		c.Fatal("Shutdown did not return after the snapshot was written")
	}
	res := <-done
	c.Assert(res.Complete, Equals, true)
	c.Assert(res.Messages, Equals, 3)
}

func (s *LogAggregatorTestSuite) TestShutdownSnapshotDrainIsBounded(c *C) {
	srv := testServer(c)
	srv.snapshotDrain = 300 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	srv.snapshots.hook = func() { close(entered); <-release }
	c.Assert(srv.Start(), IsNil)

	go func() {
		req, _ := http.NewRequest("GET", testClient(c, srv).URL+"snapshot", nil)
		req.SetBasicAuth("", testAPIKey)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	<-entered
	start := time.Now()
	srv.Shutdown()
	c.Assert(time.Since(start) < 2*time.Second, Equals, true)
}

type stubSnapshotService struct {
	discoverd.Service
	leader *discoverd.Instance
	insts  []*discoverd.Instance
}

func (s stubSnapshotService) Leader() (*discoverd.Instance, error) {
	if s.leader == nil {
		return nil, errors.New("no leader")
	}
	return s.leader, nil
}

func (s stubSnapshotService) Instances() ([]*discoverd.Instance, error) { return s.insts, nil }

func (s *LogAggregatorTestSuite) TestSnapshotSourcesLeaderFirstThenOthers(c *C) {
	a := &discoverd.Instance{Addr: "10.0.0.1:514"}
	b := &discoverd.Instance{Addr: "10.0.0.2:514"}
	c.Assert(snapshotSources(stubSnapshotService{leader: b, insts: []*discoverd.Instance{a, b}}),
		DeepEquals, []string{"http://10.0.0.2", "http://10.0.0.1"})
	// no leader (it is going away): the other instances are still tried
	c.Assert(snapshotSources(stubSnapshotService{insts: []*discoverd.Instance{a}}),
		DeepEquals, []string{"http://10.0.0.1"})
	c.Assert(snapshotSources(stubSnapshotService{}), HasLen, 0)
}

func (s *LogAggregatorTestSuite) TestLoadSnapshotCutShortAppliesNoCursors(c *C) {
	b, ends := gobRecords(c, snapMessages(4))
	err := s.srv.LoadSnapshot(bytes.NewReader(b[:ends[1]+(ends[2]-ends[1])/2]))
	c.Assert(err, NotNil)
	s.waitForLines(c, 2)
	c.Assert(s.srv.Cursors.Get(), HasLen, 0)

	c.Assert(s.srv.LoadSnapshot(bytes.NewReader(b)), IsNil)
	s.waitForLines(c, 4)
	c.Assert(s.srv.Cursors.Get(), HasLen, 1)
}

// The old leader's process exit tears down its network before the last
// bytes of a snapshot it just wrote reach the reader (seen live: the reader
// got every message but not the end, and timed out). Shutdown keeps the
// process up for snapshotLinger after the last snapshot was written.
func (s *LogAggregatorTestSuite) TestShutdownLingersAfterRecentSnapshot(c *C) {
	srv := testServer(c)
	srv.snapshotLinger = 400 * time.Millisecond
	c.Assert(srv.Start(), IsNil)
	l := testLoader(testClient(c, srv).URL)
	c.Assert(s.srv.loadSnapshotFromPeers(l).Complete, Equals, true)

	start := time.Now()
	srv.Shutdown()
	took := time.Since(start)
	c.Assert(took >= 300*time.Millisecond, Equals, true, Commentf("took %s", took))
	c.Assert(took < 3*time.Second, Equals, true, Commentf("took %s", took))
}

func (s *LogAggregatorTestSuite) TestShutdownWithoutRecentSnapshotDoesNotLinger(c *C) {
	srv := testServer(c)
	srv.snapshotLinger = 2 * time.Second
	c.Assert(srv.Start(), IsNil)
	start := time.Now()
	srv.Shutdown()
	c.Assert(time.Since(start) < time.Second, Equals, true)
}
