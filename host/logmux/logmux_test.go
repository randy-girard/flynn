package logmux

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
	"gopkg.in/natefinch/lumberjack.v2"
)

func newTestMux(t *testing.T) *Mux {
	t.Helper()
	m := New("host1", t.TempDir(), log15.New())
	t.Cleanup(m.waitIdle)
	return m
}

func TestMuxWriteSystemLine(t *testing.T) {
	m := newTestMux(t)
	ch := make(chan message, 1)
	unsub := m.subscribe("app-1", ch)
	defer unsub()
	m.Write(logagg.MsgIDSystem, &Config{
		AppID:   "app-1",
		HostID:  "host1",
		JobType: "web",
		JobID:   "host1-abc",
		JobName: "web.1",
	}, "Starting web process (runtime large)")
	select {
	case msg := <-ch:
		if string(msg.Msg) != "Starting web process (runtime large)" {
			t.Fatalf("msg=%q", msg.Msg)
		}
		if string(msg.MsgID) != string(logagg.MsgIDSystem) {
			t.Fatalf("msgid=%s", msg.MsgID)
		}
		if string(msg.ProcID) != "web.host1-abc" {
			t.Fatalf("procid=%s (must keep host job id for filters)", msg.ProcID)
		}
		if utils.StreamType(msg.Message) != logagg.StreamTypeSystem {
			t.Fatalf("stream=%s", utils.StreamType(msg.Message))
		}
		sd, err := rfc5424.ParseStructuredData(msg.StructuredData)
		if err != nil {
			t.Fatal(err)
		}
		if got := sdParam(sd, "job_name"); got != "web.1" {
			t.Fatalf("job_name=%q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log line")
	}
}

func TestMuxFollowKeepsJobIDInProcID(t *testing.T) {
	m := newTestMux(t)
	ch := make(chan message, 1)
	unsub := m.subscribe("app-1", ch)
	defer unsub()
	r, w := io.Pipe()
	stream := m.Follow(r, "", logagg.MsgIDStdout, &Config{
		AppID:   "app-1",
		HostID:  "host1",
		JobType: "web",
		JobID:   "localhost-a0ee10eb-a24b-4bab-8f58-87cada82a68c",
		JobName: "web.1",
	})
	if _, err := w.Write([]byte("hello from web\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	select {
	case msg := <-ch:
		if string(msg.ProcID) != "web.localhost-a0ee10eb-a24b-4bab-8f58-87cada82a68c" {
			t.Fatalf("procid=%s", msg.ProcID)
		}
		sd, err := rfc5424.ParseStructuredData(msg.StructuredData)
		if err != nil {
			t.Fatal(err)
		}
		if got := sdParam(sd, "job_name"); got != "web.1" {
			t.Fatalf("job_name=%q", got)
		}
		if string(msg.Msg) != "hello from web" {
			t.Fatalf("msg=%q", msg.Msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log line")
	}
	stream.Close()
}

func sdParam(sd *rfc5424.StructuredData, name string) string {
	if sd == nil {
		return ""
	}
	for _, p := range sd.Params {
		if string(p.Name) == name {
			return string(p.Value)
		}
	}
	return ""
}

func TestMuxWriteSkipsEmptyApp(t *testing.T) {
	m := newTestMux(t)
	ch := make(chan message, 1)
	unsub := m.subscribe("", ch)
	defer unsub()
	m.Write(logagg.MsgIDSystem, &Config{HostID: "host1", JobID: "j"}, "nope")
	select {
	case <-ch:
		t.Fatal("must not write without an app id")
	case <-time.After(50 * time.Millisecond):
	}
}

// A per-app follower that has stopped reading must not block the writer
// indefinitely: State.sendEvent writes lifecycle lines under the host state
// lock, and an unbounded send here hung a whole host mid-upgrade.
func TestMuxWriteDoesNotBlockOnStalledFollower(t *testing.T) {
	old := broadcastTimeout
	broadcastTimeout = 50 * time.Millisecond
	defer func() { broadcastTimeout = old }()

	m := newTestMux(t)
	stalled := make(chan message) // unbuffered and never read
	unsub := m.subscribe("app-1", stalled)
	defer unsub()
	healthy := make(chan message, 1)
	unsubHealthy := m.subscribe("app-1", healthy)
	defer unsubHealthy()

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Write(logagg.MsgIDSystem, &Config{AppID: "app-1", HostID: "host1", JobID: "j"}, "Starting web process")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked on a follower that never reads")
	}
	select {
	case <-healthy:
	case <-time.After(time.Second):
		t.Fatal("healthy follower must still receive the line")
	}
}

func TestMuxWriteDoesNotBlockOnStuckDisk(t *testing.T) {
	// The disk writer is blocked on purpose, so this test must not wait for it.
	m := New("host1", t.TempDir(), log15.New())
	m.diskWriter = func(string) io.WriteCloser { return &hangWriter{block: make(chan struct{})} }
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 32; i++ {
			m.Write(logagg.MsgIDSystem, &Config{AppID: "app-1", HostID: "host1", JobID: "j"}, "line")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked on a log file nobody is reading")
	}
}

func TestMuxFollowPromotesPluginSampleToSystem(t *testing.T) {
	m := newTestMux(t)
	ch := make(chan message, 4)
	unsub := m.subscribe("app-1", ch)
	defer unsub()
	r, w := io.Pipe()
	stream := m.Follow(r, "", logagg.MsgIDStderr, &Config{
		AppID:   "app-1",
		HostID:  "host1",
		JobType: "postgres",
		JobID:   "host1-abc",
		JobName: "postgres.5426",
	})
	defer stream.Close()
	if _, err := w.Write([]byte("ERROR:  relation foo does not exist\nflynn-postgres source=postgresql-basin-73690 sample#tables=11\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()

	var errLine, sample message
	for i := 0; i < 2; i++ {
		select {
		case msg := <-ch:
			if string(msg.MsgID) == string(logagg.MsgIDSystem) {
				sample = msg
			} else {
				errLine = msg
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for log lines")
		}
	}
	if string(errLine.MsgID) != string(logagg.MsgIDStderr) {
		t.Fatalf("engine log msgid=%s", errLine.MsgID)
	}
	if utils.StreamType(errLine.Message) != logagg.StreamTypeStderr {
		t.Fatalf("engine stream=%s", utils.StreamType(errLine.Message))
	}
	if string(sample.MsgID) != string(logagg.MsgIDSystem) {
		t.Fatalf("sample msgid=%s", sample.MsgID)
	}
	if utils.StreamType(sample.Message) != logagg.StreamTypeSystem {
		t.Fatalf("sample stream=%s", utils.StreamType(sample.Message))
	}
	if string(sample.Msg) != "flynn-postgres source=postgresql-basin-73690 sample#tables=11" {
		t.Fatalf("msg=%q", sample.Msg)
	}
}

func TestMuxFollowDoesNotStallWhenDiskIsStuck(t *testing.T) {
	// The disk writer is blocked on purpose, so this test must not wait for it.
	m := New("host1", t.TempDir(), log15.New())
	m.diskWriter = func(string) io.WriteCloser { return &hangWriter{block: make(chan struct{})} }
	ch := make(chan message, 8)
	unsub := m.subscribe("app-1", ch)
	defer unsub()
	r, w := io.Pipe()
	stream := m.Follow(r, "", logagg.MsgIDStdout, &Config{
		AppID:  "app-1",
		HostID: "host1",
		JobID:  "job-1",
	})
	defer stream.Close()
	if _, err := w.Write([]byte("one\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("Follow stalled while the on-disk log could not be written")
		}
	}
}

type hangWriter struct {
	block chan struct{}
}

func (h *hangWriter) Write(p []byte) (int, error) {
	<-h.block
	return len(p), nil
}

func (h *hangWriter) Close() error { return nil }

// A full sink buffer must not hold Follow. The firehose channel is that buffer.
// Two full subscribers used to cost two broadcast timeouts.
func TestFullSinkBufferDropsWithoutBlocking(t *testing.T) {
	old := broadcastTimeout
	broadcastTimeout = 300 * time.Millisecond
	defer func() { broadcastTimeout = old }()

	m := newTestMux(t)
	left := make(chan message, 1)
	right := make(chan message, 1)
	defer m.subscribe(firehoseApp, left)()
	defer m.subscribe(firehoseApp, right)()
	healthy := make(chan message, 4)
	defer m.subscribe("app-1", healthy)()

	m.Write(logagg.MsgIDSystem, &Config{AppID: "app-1", HostID: "host1", JobID: "j"}, "first")
	if got := m.Drops(); got != 0 {
		t.Fatalf("drops=%d, a line that fit in the sink buffer was counted", got)
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Write(logagg.MsgIDSystem, &Config{AppID: "app-1", HostID: "host1", JobID: "j"}, "second")
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Write blocked on a full sink buffer")
	}
	if got := m.Drops(); got != 2 {
		t.Fatalf("drops=%d, want 2", got)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-healthy:
		case <-time.After(time.Second):
			t.Fatal("healthy follower missed a line")
		}
	}
	if time.Since(start) >= 200*time.Millisecond {
		t.Fatal("full sink buffer held the writer")
	}
}

// N stalled live tails share one deadline. They are not the sink drop counter.
func TestStalledFollowersShareOneDeadline(t *testing.T) {
	old := broadcastTimeout
	broadcastTimeout = 200 * time.Millisecond
	defer func() { broadcastTimeout = old }()

	m := newTestMux(t)
	defer m.subscribe("app-1", make(chan message))()
	defer m.subscribe("app-1", make(chan message))()
	healthy := make(chan message, 1)
	defer m.subscribe("app-1", healthy)()

	start := time.Now()
	m.Write(logagg.MsgIDSystem, &Config{AppID: "app-1", HostID: "host1", JobID: "j"}, "line")
	if time.Since(start) >= 350*time.Millisecond {
		t.Fatalf("stalled followers stacked the wait: %s", time.Since(start))
	}
	if got := m.Drops(); got != 0 {
		t.Fatalf("follower timeout counted as a sink drop: %d", got)
	}
	select {
	case <-healthy:
	case <-time.After(time.Second):
		t.Fatal("healthy follower missed the line")
	}
}

func TestDiskQueueDropIncrementsCounter(t *testing.T) {
	// Mux.Write releases the app log after each line, so each call gets a new
	// queue. Follow holds one queue for the life of the stream. The disk
	// writer blocks inside Write after taking one line, and the queue holds
	// appLogDiskQueue more.
	m := New("host1", t.TempDir(), log15.New())
	m.diskWriter = func(string) io.WriteCloser { return &hangWriter{block: make(chan struct{})} }
	r, w := io.Pipe()
	stream := m.Follow(r, "", logagg.MsgIDStdout, &Config{
		AppID:  "app-1",
		HostID: "host1",
		JobID:  "job-1",
	})
	const extra = 5
	n := appLogDiskQueue + 1 + extra
	payload := strings.Repeat("line\n", n)
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, _ = io.WriteString(w, payload)
		w.Close()
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := m.Drops(); got >= extra {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("drops=%d, want at least %d", m.Drops(), extra)
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-writeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("log write did not finish")
	}
	stream.Close()
}

func TestHostLogFileBounds(t *testing.T) {
	m := newTestMux(t)
	l := m.appLog("app-1")
	defer l.Release()
	lj, ok := l.w.(*lumberjack.Logger)
	if !ok {
		t.Fatalf("writer %T", l.w)
	}
	if lj.MaxSize != 100 || lj.MaxBackups != 1 {
		t.Fatalf("MaxSize=%d MaxBackups=%d, want 100 and 1", lj.MaxSize, lj.MaxBackups)
	}
}

type scriptSink struct {
	mu         sync.Mutex
	connects   []time.Time
	writeErrAt time.Time
	shutdown   chan struct{}
	once       sync.Once
}

func newScriptSink() *scriptSink {
	return &scriptSink{shutdown: make(chan struct{})}
}

func (s *scriptSink) Info() *SinkInfo { return &SinkInfo{} }
func (s *scriptSink) Name() string    { return "script" }
func (s *scriptSink) Connect() error {
	s.mu.Lock()
	s.connects = append(s.connects, time.Now())
	n := len(s.connects)
	s.mu.Unlock()
	if n >= 2 {
		s.Shutdown()
	}
	return nil
}
func (s *scriptSink) Close()                                      {}
func (s *scriptSink) GetCursor(string) (*utils.HostCursor, error) { return nil, nil }
func (s *scriptSink) Write(message) error {
	s.mu.Lock()
	if s.writeErrAt.IsZero() {
		s.writeErrAt = time.Now()
	}
	s.mu.Unlock()
	return errors.New("bad template")
}
func (s *scriptSink) Shutdown()                 { s.once.Do(func() { close(s.shutdown) }) }
func (s *scriptSink) ShutdownCh() chan struct{} { return s.shutdown }

func TestSinkWriteErrorReconnectsAfterDelay(t *testing.T) {
	old := sinkReconnectDelay
	sinkReconnectDelay = 80 * time.Millisecond
	defer func() { sinkReconnectDelay = old }()

	m := New("host1", t.TempDir(), log15.New())
	sink := newScriptSink()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.addSink(sink)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.connects)
		sink.mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sink never connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		sink.mu.Lock()
		n := len(sink.connects)
		sink.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sink did not reconnect after a write error")
		}
		m.Write(logagg.MsgIDSystem, &Config{AppID: "app-1", HostID: "host1", JobID: "j"}, "line")
		time.Sleep(5 * time.Millisecond)
	}
	sink.mu.Lock()
	if sink.writeErrAt.IsZero() {
		sink.mu.Unlock()
		t.Fatal("sink write was not called")
	}
	gap := sink.connects[1].Sub(sink.writeErrAt)
	sink.mu.Unlock()
	if gap < 50*time.Millisecond {
		t.Fatalf("reconnected %s after the write error, want the reconnect delay", gap)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("addSink did not return after shutdown")
	}
}
