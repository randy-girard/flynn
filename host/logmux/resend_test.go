package logmux

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	host "github.com/randy-girard/flynn/host/types"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
)

// aggSink is a fake logaggregator: it keeps every line it is sent and, like
// logaggregator's HostCursors, reports the newest cursor it has received
// (parsed back from the wire bytes) as the host's cursor. fail, when set, is
// asked before each write and may make it fail.
type aggSink struct {
	mu       sync.Mutex
	got      []aggLine
	cursor   *utils.HostCursor
	connects int
	writes   int
	fail     func(s *aggSink, l aggLine) error
	delay    func(l aggLine) time.Duration
	shutdown chan struct{}
	once     sync.Once
}

type aggLine struct {
	app, text string
	at        time.Time
}

func newAggSink() *aggSink { return &aggSink{shutdown: make(chan struct{})} }

func (s *aggSink) Info() *SinkInfo { return &SinkInfo{} }
func (s *aggSink) Name() string    { return "agg" }
func (s *aggSink) Connect() error {
	s.mu.Lock()
	s.connects++
	s.mu.Unlock()
	return nil
}
func (s *aggSink) Close() {}
func (s *aggSink) GetCursor(string) (*utils.HostCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursor == nil {
		return nil, nil
	}
	c := *s.cursor
	return &c, nil
}
func (s *aggSink) Write(m message) error {
	msg, cursor, err := utils.ParseMessage(m.Message.Bytes())
	if err != nil {
		return err
	}
	l := aggLine{app: string(msg.AppName), text: string(msg.Msg)}
	if s.delay != nil {
		if d := s.delay(l); d > 0 {
			time.Sleep(d)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	if s.fail != nil {
		if err := s.fail(s, l); err != nil {
			return err
		}
	}
	l.at = time.Now()
	s.got = append(s.got, l)
	if s.cursor == nil || cursor.After(*s.cursor) {
		s.cursor = cursor
	}
	return nil
}
func (s *aggSink) Shutdown()                 { s.once.Do(func() { close(s.shutdown) }) }
func (s *aggSink) ShutdownCh() chan struct{} { return s.shutdown }

func (s *aggSink) lines(app string) []aggLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res []aggLine
	for _, l := range s.got {
		if l.app == app {
			res = append(res, l)
		}
	}
	return res
}

func (s *aggSink) hasApp(app string) bool {
	return len(s.lines(app)) > 0
}

// Log files are only resent for app ids that look like UUIDs.
const (
	appA    = "aaaaaaaa-0000-4000-8000-00000000000a"
	appB    = "bbbbbbbb-0000-4000-8000-00000000000b"
	appLive = "cccccccc-0000-4000-8000-00000000000c"
)

// errWriteTimeout is what a TCP write past its deadline returns.
var errWriteTimeout error = &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}

// writeHistory logs n lines for app ("<app>-<i>", i from 1) and waits until
// they are in the app's log file, as if the host had run the app before the
// sink was added.
func writeHistory(t *testing.T, m *Mux, app string, n int) {
	t.Helper()
	// One job's stdout, so the file is in line order.
	r, w := io.Pipe()
	stream := m.Follow(r, "", logagg.MsgIDStdout, &Config{AppID: app, HostID: "host1", JobID: "job-" + app})
	for i := 1; i <= n; i++ {
		fmt.Fprintf(w, "%s-%d\n", app, i)
	}
	w.Close()
	<-stream.done
	m.waitIdle()
}

// checkExactlyOnce fails unless app's lines 1..n each arrived exactly once,
// and, when ordered, in order.
func checkExactlyOnce(t *testing.T, s *aggSink, app string, n int, ordered bool) {
	t.Helper()
	seen := make(map[int]int)
	last := 0
	outOfOrder := 0
	for _, l := range s.lines(app) {
		if strings.HasSuffix(l.text, "-live") {
			continue // checked by the test itself
		}
		i, err := strconv.Atoi(strings.TrimPrefix(l.text, app+"-"))
		if err != nil {
			t.Fatalf("unexpected line %q", l.text)
		}
		seen[i]++
		if i < last {
			outOfOrder++
		}
		last = i
	}
	var missing, dups []int
	for i := 1; i <= n; i++ {
		switch seen[i] {
		case 0:
			missing = append(missing, i)
		case 1:
		default:
			dups = append(dups, i)
		}
	}
	if len(missing) > 0 || len(dups) > 0 {
		t.Errorf("%s: %d of %d lines missing (first %v), %d duplicated (first %v)", app, len(missing), n, head(missing), len(dups), head(dups))
	}
	if ordered && outOfOrder > 0 {
		t.Errorf("%s: %d lines arrived out of order", app, outOfOrder)
	}
}

func head(s []int) []int {
	if len(s) > 5 {
		return s[:5]
	}
	return s
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startSink(t *testing.T, m *Mux, s *aggSink) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.addSink(s)
	}()
	t.Cleanup(func() {
		s.Shutdown()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("addSink did not return after shutdown")
		}
	})
}

// liveWriter logs "<app>-<i>" every interval until stop is closed and returns
// how many lines it wrote.
func liveWriter(m *Mux, app string, interval time.Duration, stop chan struct{}) <-chan int {
	n := make(chan int, 1)
	go func() {
		i := 0
		defer func() { n <- i }()
		for {
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
			i++
			m.Write(logagg.MsgIDSystem, &Config{AppID: app, HostID: "host1", JobID: "job-" + app}, fmt.Sprintf("%s-%d", app, i))
		}
	}()
	return n
}

// EDGE-84: a logaggregator with no cursor for the host gets the host's whole
// log files. One write timeout in the middle used to reconnect and take the
// aggregator's newest cursor, which a live line had already moved past every
// line not yet resent, so the rest of the resend was skipped for good.
func TestResendResumesAfterWriteTimeout(t *testing.T) {
	old := sinkReconnectDelay
	sinkReconnectDelay = 20 * time.Millisecond
	defer func() { sinkReconnectDelay = old }()

	m := New("host1", t.TempDir(), log15.New())
	writeHistory(t, m, appA, 400)
	writeHistory(t, m, appB, 400)

	s := newAggSink()
	// A slow aggregator: the resend takes a while, so live lines go out
	// in the middle of it.
	s.delay = func(l aggLine) time.Duration {
		if l.app != appLive {
			return 500 * time.Microsecond
		}
		return 0
	}
	// Time out once, on the first resent line after a live line arrived.
	failed := false
	s.fail = func(s *aggSink, l aggLine) error {
		if failed || l.app == appLive {
			return nil
		}
		for _, g := range s.got {
			if g.app == appLive {
				failed = true
				return errWriteTimeout
			}
		}
		return nil
	}
	startSink(t, m, s)
	waitFor(t, 5*time.Second, "the first write", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.writes > 0
	})
	stop := make(chan struct{})
	liveN := liveWriter(m, appLive, 2*time.Millisecond, stop)

	waitFor(t, 5*time.Second, "the write timeout", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return failed
	})
	// Give the resend time to finish (or not).
	time.Sleep(200 * time.Millisecond)
	waitFor(t, 5*time.Second, "a reconnect", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.connects >= 2
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (len(s.lines(appA)) < 400 || len(s.lines(appB)) < 400) {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	n := <-liveN
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(s.lines(appLive)) < n {
		time.Sleep(10 * time.Millisecond)
	}
	m.waitIdle()

	checkExactlyOnce(t, s, appA, 400, true)
	checkExactlyOnce(t, s, appB, 400, true)
	checkExactlyOnce(t, s, appLive, n, false)
}

// EDGE-84: during a resend, a live line of an app whose files are not resent
// yet was dropped (it would come later from the file), so `flynn log` was as
// far behind as the resend. Live lines go out at once.
func TestLiveLineIsNotHeldBehindResend(t *testing.T) {
	m := New("host1", t.TempDir(), log15.New())
	writeHistory(t, m, appA, 3000)

	s := newAggSink()
	s.delay = func(l aggLine) time.Duration {
		if strings.HasSuffix(l.text, "-live") {
			return 0
		}
		return 500 * time.Microsecond // about 1.5 s for the whole resend
	}
	startSink(t, m, s)
	waitFor(t, 5*time.Second, "the resend to start", func() bool { return s.hasApp(appA) })

	sent := time.Now()
	m.Write(logagg.MsgIDSystem, &Config{AppID: appA, HostID: "host1", JobID: "job-a"}, appA+"-live")
	var at time.Time
	var before int
	deadline := time.Now().Add(5 * time.Second)
	for at.IsZero() && time.Now().Before(deadline) {
		for i, l := range s.lines(appA) {
			if l.text == appA+"-live" {
				at, before = l.at, i
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if at.IsZero() {
		t.Fatal("the live line never arrived")
	}
	if lag := at.Sub(sent); lag > 300*time.Millisecond {
		t.Errorf("live line arrived %s after it was logged (after %d resent lines), want it ahead of the resend", lag, before)
	}
	if before >= 3000 {
		t.Errorf("live line arrived after the whole resend")
	}
	waitFor(t, 10*time.Second, "the resend to finish", func() bool { return len(s.lines(appA)) >= 3001 })
	m.waitIdle()
	got := 0
	for _, l := range s.lines(appA) {
		if l.text == appA+"-live" {
			got++
		}
	}
	if got != 1 {
		t.Errorf("live line arrived %d times, want once", got)
	}
	checkExactlyOnce(t, s, appA, 3000, true)
}

// EDGE-84: live lines that do not fit in the sink buffer while the
// aggregator is slow are sent later from the log file, once each.
func TestOverflowedLiveLinesAreResentFromFile(t *testing.T) {
	m := New("host1", t.TempDir(), log15.New())
	s := newAggSink()
	var block sync.Once
	s.delay = func(l aggLine) time.Duration {
		d := time.Duration(0)
		block.Do(func() { d = 400 * time.Millisecond })
		return d
	}
	startSink(t, m, s)
	time.Sleep(50 * time.Millisecond) // connected, nothing to resend

	const n = 1000 // more than the sink buffer
	for i := 1; i <= n; i++ {
		m.Write(logagg.MsgIDSystem, &Config{AppID: appA, HostID: "host1", JobID: "job-a"}, fmt.Sprintf(appA+"-%d", i))
	}
	if m.Drops() == 0 {
		t.Fatal("the sink buffer never filled, the test did not overflow it")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(s.lines(appA)) < n {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	m.waitIdle()
	checkExactlyOnce(t, s, appA, n, false)
}

// EDGE-84: a write that passes its first deadline goes on with the rest of
// the frame under a longer one instead of failing the connection.
func TestLogAggregatorSinkWriteRetriesAfterTimeout(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	sm := NewSinkManager("", nil, stubJobs{}, log15.New())
	s := &LogAggregatorSink{sm: sm, conn: client, shutdownCh: make(chan struct{})}

	m := New("host1", t.TempDir(), log15.New())
	ch := make(chan message, 1)
	defer m.subscribe(firehoseApp, ch)()
	m.Write(logagg.MsgIDSystem, &Config{AppID: appA, HostID: "host1", JobID: "job-a"}, "hello")
	msg := <-ch
	m.waitIdle()

	read := make(chan []byte, 1)
	go func() {
		time.Sleep(1300 * time.Millisecond) // past the first deadline
		b, _ := io.ReadAll(server)
		read <- b
	}()
	if err := s.Write(msg); err != nil {
		t.Fatalf("write failed after a stall shorter than the retry budget: %v", err)
	}
	client.Close()
	b := <-read
	if !strings.HasSuffix(string(b), " hello") || !strings.HasPrefix(string(b), strconv.Itoa(len(msg.Message.Bytes()))+" ") {
		t.Fatalf("frame=%q, want one whole rfc6587 frame", b)
	}
}

// EDGE-84: a write to an aggregator that stays stuck still fails, within the
// retry budget, so the host reconnects.
func TestLogAggregatorSinkWriteGivesUpOnStuckAggregator(t *testing.T) {
	prev := sinkWriteTimeouts
	sinkWriteTimeouts = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	defer func() { sinkWriteTimeouts = prev }()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	sm := NewSinkManager("", nil, stubJobs{}, log15.New())
	s := &LogAggregatorSink{sm: sm, conn: client, shutdownCh: make(chan struct{})}
	m := New("host1", t.TempDir(), log15.New())
	ch := make(chan message, 1)
	defer m.subscribe(firehoseApp, ch)()
	m.Write(logagg.MsgIDSystem, &Config{AppID: appA, HostID: "host1", JobID: "job-a"}, "hello")
	msg := <-ch
	m.waitIdle()
	start := time.Now()
	err := s.Write(msg)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("err=%v, want a timeout", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("gave up after %s, want about the sum of the deadlines", d)
	}
}

// The syslog sink's cursor is the newest line it wrote. With live lines and
// resent lines interleaved, the last line written can be an older one.
func TestSyslogSinkCursorIsNewestLineWritten(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go io.Copy(io.Discard, server)
	sm := NewSinkManager("", nil, stubJobs{}, log15.New())
	s, err := NewSyslogSink(sm, &SinkInfo{Config: []byte(`{"url":"syslog://127.0.0.1:1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	s.conn = client
	m := New("host1", t.TempDir(), log15.New())
	ch := make(chan message, 2)
	defer m.subscribe(firehoseApp, ch)()
	m.Write(logagg.MsgIDSystem, &Config{AppID: appA, HostID: "host1", JobID: "j"}, "old")
	m.Write(logagg.MsgIDSystem, &Config{AppID: appA, HostID: "host1", JobID: "j"}, "new")
	older, newer := <-ch, <-ch
	m.waitIdle()
	if err := s.Write(newer); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(older); err != nil {
		t.Fatal(err)
	}
	c, _ := s.GetCursor("host1")
	if c == nil || c.Seq != newer.Seq {
		t.Fatalf("cursor=%v, want the newest line written (seq %d)", c, newer.Seq)
	}
}

type stubJobs struct{}

func (stubJobs) GetJob(string) *host.ActiveJob { return nil }

func TestResendStateRanges(t *testing.T) {
	at := func(sec int) utils.HostCursor {
		return utils.HostCursor{Time: time.Unix(int64(sec), 0), Seq: uint64(sec)}
	}
	ptr := func(c utils.HostCursor) *utils.HostCursor { return &c }

	r := newResendState()
	r.connected([]string{"a"}, nil, at(100))
	if !r.wants("a", at(1)) || !r.wants("a", at(100)) || r.wants("a", at(101)) || r.wants("b", at(1)) {
		t.Fatal("a sink with no cursor wants every line up to the subscription")
	}
	// A line written live inside the range is not resent.
	r.wroteLive("a", at(50))
	if r.wants("a", at(50)) {
		t.Fatal("a line written live was wanted again")
	}
	// Resent up to 80: lines before 80-resendSlack are done.
	r.wroteResent("a", at(80))
	if r.wants("a", at(60)) || r.wants("a", at(80)) || !r.wants("a", at(75)) {
		t.Fatal("resent lines must be done, lines in the slack window not yet written must not")
	}
	// Reconnect: the sink's cursor is a live line at 200. Lines after it up
	// to the new subscription are added, the rest of the resend stays.
	if !r.connected([]string{"a"}, ptr(at(200)), at(300)) {
		t.Fatal("reconnect did not resume the resend")
	}
	if !r.wants("a", at(90)) || r.wants("a", at(150)) || !r.wants("a", at(250)) {
		t.Fatal("reconnect lost the pending resend or resent what the sink has")
	}
	// A sink whose cursor went back is a new aggregator: everything again.
	r.connected([]string{"a"}, ptr(at(10)), at(400))
	if r.wants("a", at(5)) || !r.wants("a", at(11)) || !r.wants("a", at(150)) {
		t.Fatal("a cursor older than last time must restart the resend from it")
	}
	gen, _ := r.snapshot()
	if finished, _, _ := r.passDone(gen); !finished || r.wants("a", at(150)) {
		t.Fatal("a full pass must finish the resend")
	}
}
