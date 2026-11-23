package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/logaggregator/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
	"github.com/randy-girard/flynn/pkg/syslog/rfc6587"
)

type ServerTestSuite struct{}

var _ = Suite(&ServerTestSuite{})

func (s *ServerTestSuite) TestServerDurability(c *C) {
	srv := testServer(c)
	c.Assert(srv.Start(), IsNil)
	defer srv.Shutdown()

	cl := testClient(c, srv)

	conn, err := net.Dial("tcp", srv.SyslogAddr().String())
	c.Assert(err, IsNil)
	defer conn.Close()

	zero := 0
	rc, err := cl.GetLog("app-A", &logagg.LogOpts{Follow: true, Lines: &zero})
	c.Assert(err, IsNil)

	for _, msg := range appAMessages {
		conn.Write(rfc6587.Bytes(msg))
	}

	var got client.Message
	dec := json.NewDecoder(rc)
	for _, want := range appAMessages {
		c.Assert(dec.Decode(&got), IsNil)
		c.Assert(got.HostID, Equals, string(want.Hostname))
		c.Assert(got.Stream, Equals, utils.StreamType(want))
		c.Assert(got.Timestamp.Equal(want.Timestamp), Equals, true)

		procType, jobID := splitProcID(want.ProcID)
		c.Assert(got.ProcessType, Equals, string(procType))
		c.Assert(got.JobID, Equals, string(jobID))
	}
}

func (s *ServerTestSuite) TestHostCursors(c *C) {
	srv := testServer(c)
	srv.testMessageHook = make(chan struct{}, 1)
	c.Assert(srv.Start(), IsNil)
	defer srv.Shutdown()
	cl := testClient(c, srv)

	conn, err := net.Dial("tcp", srv.SyslogAddr().String())
	c.Assert(err, IsNil)
	defer conn.Close()

	assertCursors := func(expected map[string]utils.HostCursor) {
		cursors, err := cl.GetCursors()
		c.Assert(err, IsNil)
		c.Assert(cursors, DeepEquals, expected)
	}
	write := func(msg *rfc5424.Message) {
		conn.Write(rfc6587.Bytes(msg))
		<-srv.testMessageHook
	}

	// write some messages
	msg1, msg2 := newSeqMessage("host1", 1, 0), newSeqMessage("host2", 1, 0)
	write(msg1)
	write(msg2)

	assertCursors(map[string]utils.HostCursor{
		"host1": {Time: msg1.Timestamp, Seq: 1},
		"host2": {Time: msg2.Timestamp, Seq: 1},
	})

	// test new timestamp with seq rolled over
	msg3 := newSeqMessage("host1", 1, 1)
	write(msg3)
	assertCursors(map[string]utils.HostCursor{
		"host1": {Time: msg3.Timestamp, Seq: 1},
		"host2": {Time: msg2.Timestamp, Seq: 1},
	})

	// test same timestamp with new seq
	msg4 := newSeqMessage("host1", 2, 1)
	write(msg4)
	assertCursors(map[string]utils.HostCursor{
		"host1": {Time: msg4.Timestamp, Seq: 2},
		"host2": {Time: msg2.Timestamp, Seq: 1},
	})
}

// TestBadFrameClosesOnlyThatConnection sends a negative MSG-LEN frame, which
// used to panic in the split function and exit the process. Only the
// offending connection may be closed; the server and other connections must
// keep working.
func (s *ServerTestSuite) TestBadFrameClosesOnlyThatConnection(c *C) {
	srv := testServer(c)
	srv.testMessageHook = make(chan struct{}, 1)
	c.Assert(srv.Start(), IsNil)
	defer srv.Shutdown()
	cl := testClient(c, srv)

	good, err := net.Dial("tcp", srv.SyslogAddr().String())
	c.Assert(err, IsNil)
	defer good.Close()

	msg1 := newSeqMessage("host1", 1, 0)
	good.Write(rfc6587.Bytes(msg1))
	waitMessage(c, srv)

	for _, frame := range []string{"-5 x", "-3 abcdef", "0 x"} {
		bad, err := net.Dial("tcp", srv.SyslogAddr().String())
		c.Assert(err, IsNil)
		_, err = bad.Write([]byte(frame))
		c.Assert(err, IsNil)
		assertClosedByServer(c, bad)
		bad.Close()
	}

	// a well-framed but truncated RFC5424 message is skipped and the
	// connection stays open
	good.Write([]byte("25 <0>1 0000-01-01T0:00:00Z "))
	waitMessage(c, srv)

	// the existing connection still works
	msg2 := newSeqMessage("host1", 2, 0)
	good.Write(rfc6587.Bytes(msg2))
	waitMessage(c, srv)

	// and new connections are still accepted
	other, err := net.Dial("tcp", srv.SyslogAddr().String())
	c.Assert(err, IsNil)
	defer other.Close()
	msg3 := newSeqMessage("host2", 1, 0)
	other.Write(rfc6587.Bytes(msg3))
	waitMessage(c, srv)

	cursors, err := cl.GetCursors()
	c.Assert(err, IsNil)
	c.Assert(cursors, DeepEquals, map[string]utils.HostCursor{
		"host1": {Time: msg2.Timestamp, Seq: 2},
		"host2": {Time: msg3.Timestamp, Seq: 1},
	})
}

// TestPanicInConnectionIsContained checks that a panic while draining one
// syslog connection closes that connection without exiting the process.
func (s *ServerTestSuite) TestPanicInConnectionIsContained(c *C) {
	srv := testServer(c)
	srv.testMessageHook = make(chan struct{}, 1)
	srv.syslogSplit = func(data []byte, atEOF bool) (int, []byte, error) {
		if len(data) > 0 && data[0] == '!' {
			panic("boom")
		}
		return rfc6587.Split(data, atEOF)
	}
	c.Assert(srv.Start(), IsNil)
	defer srv.Shutdown()

	bad, err := net.Dial("tcp", srv.SyslogAddr().String())
	c.Assert(err, IsNil)
	defer bad.Close()
	_, err = bad.Write([]byte("!"))
	c.Assert(err, IsNil)
	assertClosedByServer(c, bad)

	good, err := net.Dial("tcp", srv.SyslogAddr().String())
	c.Assert(err, IsNil)
	defer good.Close()
	good.Write(rfc6587.Bytes(newSeqMessage("host1", 1, 0)))
	waitMessage(c, srv)
}

func (s *ServerTestSuite) TestSyslogFramingErrorOnlyForBadFrames(c *C) {
	_, _, err := rfc6587.Split([]byte("-5 x"), false)
	c.Assert(isSyslogFramingError(err), Equals, true)
	c.Assert(isSyslogFramingError(nil), Equals, false)
	c.Assert(isSyslogFramingError(&net.OpError{Op: "read", Net: "tcp", Err: io.ErrUnexpectedEOF}), Equals, false)
	c.Assert(isSyslogFramingError(fmt.Errorf("wrapped: %w", &net.OpError{Op: "read", Net: "tcp", Err: io.EOF})), Equals, false)
}

func waitMessage(c *C, srv *Server) {
	select {
	case <-srv.testMessageHook:
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for the server to read a syslog message")
	}
}

func assertClosedByServer(c *C, conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := bufio.NewReader(conn).ReadByte()
	c.Assert(err, Equals, io.EOF)
}

func newSeqMessage(hostname string, seq, timeDiff int) *rfc5424.Message {
	m := rfc5424.NewMessage(
		&rfc5424.Header{
			AppName:   []byte("foo"),
			ProcID:    []byte("bar"),
			Timestamp: time.Date(2013, 7, 17, 16, 43, 41, 0, time.UTC).Add(time.Duration(timeDiff) * time.Second),
			Hostname:  []byte(hostname),
		},
		[]byte("asdf"),
	)
	m.StructuredData = []byte(fmt.Sprintf(`[flynn seq="%d"]`, seq))
	return m
}
