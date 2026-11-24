package logmux

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
	"github.com/randy-girard/flynn/pkg/syslog/rfc6587"
)

// fileLine is one line written straight into a log file: "<app>-<n>" at at.
type fileLine struct {
	n  int
	at time.Time
}

var fileSeq uint64

// writeLogFile writes lines to path in the order given, framed as appLog
// writes them, and sets the file's mtime.
func writeLogFile(t *testing.T, path, app string, lines []fileLine, mtime time.Time) {
	t.Helper()
	var b bytes.Buffer
	for _, l := range lines {
		hdr := &rfc5424.Header{
			Timestamp: l.at.UTC(),
			Hostname:  []byte("host1"),
			AppName:   []byte(app),
			ProcID:    []byte("ticker.job-" + app),
			MsgID:     []byte(logagg.MsgIDStdout),
			Severity:  6,
			Facility:  23,
		}
		msg := rfc5424.NewMessage(hdr, []byte(fmt.Sprintf("%s-%d", app, l.n)))
		var sd bytes.Buffer
		// seq is unique per host, as the mux's counter makes it.
		flynnStructuredData(atomic.AddUint64(&fileSeq, 1), "ticker.1").Encode(&sd)
		msg.StructuredData = sd.Bytes()
		b.Write(rfc6587.Bytes(msg))
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, b.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// seqLines returns lines lo..hi, one every step, the last at end.
func seqLines(lo, hi int, end time.Time, step time.Duration) []fileLine {
	res := make([]fileLine, 0, hi-lo+1)
	for n := lo; n <= hi; n++ {
		res = append(res, fileLine{n, end.Add(-time.Duration(hi-n) * step)})
	}
	return res
}

// countReads counts the bytes the resend reads from log files.
func countReads(t *testing.T) *int64 {
	t.Helper()
	var n int64
	prev := openResendFile
	openResendFile = func(name string) (resendFile, error) {
		f, err := prev(name)
		if err != nil {
			return nil, err
		}
		return &countingFile{resendFile: f, n: &n}, nil
	}
	t.Cleanup(func() { openResendFile = prev })
	return &n
}

type countingFile struct {
	resendFile
	n *int64
}

func (f *countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := f.resendFile.ReadAt(p, off)
	atomic.AddInt64(f.n, int64(n))
	return n, err
}

func (f *countingFile) Read(p []byte) (int, error) {
	n, err := f.resendFile.Read(p)
	atomic.AddInt64(f.n, int64(n))
	return n, err
}

// checkSet fails unless app's lines are exactly want, each once, and, when
// ordered, in increasing order.
func checkSet(t *testing.T, s *aggSink, app string, want []int, ordered bool) {
	t.Helper()
	wantSet := make(map[int]bool, len(want))
	for _, n := range want {
		wantSet[n] = true
	}
	seen := make(map[int]int)
	var extra []int
	last, outOfOrder := 0, 0
	for _, l := range s.lines(app) {
		n, err := strconv.Atoi(strings.TrimPrefix(l.text, app+"-"))
		if err != nil {
			t.Fatalf("unexpected line %q", l.text)
		}
		seen[n]++
		if !wantSet[n] {
			extra = append(extra, n)
		}
		if n < last {
			outOfOrder++
		}
		last = n
	}
	var missing, dups []int
	for _, n := range want {
		switch seen[n] {
		case 0:
			missing = append(missing, n)
		case 1:
		default:
			dups = append(dups, n)
		}
	}
	sort.Ints(extra)
	if len(missing) > 0 || len(dups) > 0 || len(extra) > 0 {
		t.Errorf("%s: %d of %d wanted lines missing (first %v), %d duplicated (first %v), %d not wanted sent (first %v)",
			app, len(missing), len(want), head(missing), len(dups), head(dups), len(extra), head(extra))
	}
	if ordered && outOfOrder > 0 {
		t.Errorf("%s: %d lines arrived out of order", app, outOfOrder)
	}
}

func rangeInts(lo, hi int) []int {
	res := make([]int, 0, hi-lo+1)
	for n := lo; n <= hi; n++ {
		res = append(res, n)
	}
	return res
}

// settle waits until the sink has taken no line for d.
func settle(t *testing.T, s *aggSink, d, max time.Duration) {
	t.Helper()
	deadline := time.Now().Add(max)
	last := -1
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := s.writes
		s.mu.Unlock()
		if n == last {
			return
		}
		last = n
		time.Sleep(d)
	}
	t.Fatalf("the sink was still taking lines after %s", max)
}

// EDGE-85: an aggregator keeps only the newest lines of each app (its ring),
// so a sink with no cursor is sent only those (here 10,000 of 60,000), and the log file is read from
// its end, not from its first byte.
func TestCursorlessResendSendsOnlyNewestLinesPerApp(t *testing.T) {
	dir := t.TempDir()
	m := New("host1", dir, log15.New())
	now := time.Now()
	writeLogFile(t, filepath.Join(dir, appA+".log"), appA, seqLines(1, 60000, now.Add(-time.Minute), 10*time.Millisecond), now.Add(-time.Minute))
	fi, err := os.Stat(filepath.Join(dir, appA+".log"))
	if err != nil {
		t.Fatal(err)
	}
	read := countReads(t)

	s := newAggSink()
	s.limits = resendLimits{Lines: 10000}
	startSink(t, m, s)
	waitFor(t, 10*time.Second, "the newest line", func() bool {
		for _, l := range s.lines(appA) {
			if l.text == appA+"-60000" {
				return true
			}
		}
		return false
	})
	settle(t, s, 100*time.Millisecond, 10*time.Second)
	checkSet(t, s, appA, rangeInts(50001, 60000), true)
	// The newest sixth of the file, read backward then forward, plus the
	// lines within resendSlack before it and a chunk or two.
	if n := atomic.LoadInt64(read); n > fi.Size()/2 {
		t.Errorf("read %d bytes of a %d byte file to send its newest sixth", n, fi.Size())
	}
}

// EDGE-85: an app whose newest line is older than the aggregator's buffer TTL
// would only get a ring that expires at once, and an app the controller no
// longer has would get a ring it never drops (its deletion may be older than
// the aggregator's catch-up). Neither is sent. An app that just started (not
// in the list fetched yet) still is.
func TestCursorlessResendSkipsExpiredAndDeletedApps(t *testing.T) {
	const (
		appOld = "dddddddd-0000-4000-8000-00000000000d"
		appDel = "eeeeeeee-0000-4000-8000-00000000000e"
		appNew = "ffffffff-0000-4000-8000-00000000000f"
	)
	now := time.Now()
	write := func(dir, app string, end time.Time) {
		writeLogFile(t, filepath.Join(dir, app+".log"), app, seqLines(1, 100, end, time.Second), end)
	}
	setup := func() (*Mux, *aggSink) {
		dir := t.TempDir()
		write(dir, appA, now.Add(-2*time.Hour))
		write(dir, appOld, now.Add(-200*time.Hour))
		write(dir, appDel, now.Add(-2*time.Hour))
		write(dir, appNew, now)
		return New("host1", dir, log15.New()), newAggSink()
	}

	t.Run("limits", func(t *testing.T) {
		m, s := setup()
		s.limits = resendLimits{Lines: 10000, MaxAge: 168 * time.Hour}
		s.known = map[string]bool{appA: true, appOld: true}
		startSink(t, m, s)
		waitFor(t, 10*time.Second, "the live apps", func() bool {
			return len(s.lines(appA)) >= 100 && len(s.lines(appNew)) >= 100
		})
		settle(t, s, 100*time.Millisecond, 10*time.Second)
		checkSet(t, s, appA, rangeInts(1, 100), true)
		checkSet(t, s, appNew, rangeInts(1, 100), true)
		checkSet(t, s, appOld, nil, false)
		checkSet(t, s, appDel, nil, false)
	})

	// The controller cannot be asked: nothing is skipped as deleted. An
	// aggregator that does not advertise a TTL may keep any line.
	t.Run("unknown", func(t *testing.T) {
		m, s := setup()
		s.limits = resendLimits{Lines: 10000}
		s.knownErr = io.ErrUnexpectedEOF
		startSink(t, m, s)
		waitFor(t, 10*time.Second, "every app", func() bool {
			return len(s.lines(appA)) >= 100 && len(s.lines(appNew)) >= 100 &&
				len(s.lines(appOld)) >= 100 && len(s.lines(appDel)) >= 100
		})
		settle(t, s, 100*time.Millisecond, 10*time.Second)
		for _, app := range []string{appA, appOld, appDel, appNew} {
			checkSet(t, s, app, rangeInts(1, 100), true)
		}
	})
}

// EDGE-85: the newest lines can be spread over the files the host keeps for
// an app: logrotate's uncompressed copy (<id>.log.1), lumberjack's backup
// (<id>-<time>.log) and the current file. Compressed copies are not read.
func TestTailResendAcrossRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	m := New("host1", dir, log15.New())
	now := time.Now()
	t1, t2, t3 := now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Hour)
	writeLogFile(t, filepath.Join(dir, appA+".log.1"), appA, seqLines(1, 6000, t1, time.Millisecond), t1)
	writeLogFile(t, filepath.Join(dir, appA+"-2026-11-24T10-00-00.000.log"), appA, seqLines(6001, 9000, t2, time.Millisecond), t2)
	writeLogFile(t, filepath.Join(dir, appA+".log"), appA, seqLines(9001, 12000, t3, time.Millisecond), t3)
	if err := os.WriteFile(filepath.Join(dir, appA+".log.2.gz"), []byte("not a log"), 0644); err != nil {
		t.Fatal(err)
	}
	// Another app whose whole history fits.
	writeLogFile(t, filepath.Join(dir, appB+".log.1"), appB, seqLines(1, 50, t1, time.Second), t1)
	writeLogFile(t, filepath.Join(dir, appB+".log"), appB, seqLines(51, 100, t3, time.Second), t3)

	s := newAggSink()
	s.limits = resendLimits{Lines: 10000}
	startSink(t, m, s)
	waitFor(t, 10*time.Second, "both apps", func() bool {
		return len(s.lines(appA)) >= 10000 && len(s.lines(appB)) >= 100
	})
	settle(t, s, 100*time.Millisecond, 10*time.Second)
	checkSet(t, s, appA, rangeInts(2001, 12000), true)
	checkSet(t, s, appB, rangeInts(1, 100), true)
}

// EDGE-85: lines of one app can sit slightly out of time order in its file
// (several jobs append to it). The aggregator keeps the newest lines by
// time, so those are the ones sent, whatever their place in the file.
func TestTailResendKeepsNewestByTimeWithOutOfOrderLines(t *testing.T) {
	dir := t.TempDir()
	m := New("host1", dir, log15.New())
	base := time.Now().Add(-time.Hour)
	const total, limit = 3000, 1000
	lines := make([]fileLine, 0, total)
	for n := 1; n <= total; n++ {
		// 10 ms apart, each up to 2 s early or late (deterministic).
		jitter := time.Duration((n*7919)%4001-2000) * time.Millisecond
		lines = append(lines, fileLine{n, base.Add(time.Duration(n)*10*time.Millisecond + jitter)})
	}
	writeLogFile(t, filepath.Join(dir, appA+".log"), appA, lines, base.Add(time.Minute))
	byTime := append([]fileLine(nil), lines...)
	sort.Slice(byTime, func(i, j int) bool { return byTime[i].at.After(byTime[j].at) })
	var want []int
	for _, l := range byTime[:limit] {
		want = append(want, l.n)
	}
	sort.Ints(want)

	s := newAggSink()
	s.limits = resendLimits{Lines: limit}
	startSink(t, m, s)
	waitFor(t, 10*time.Second, "the resend", func() bool { return len(s.lines(appA)) >= limit })
	settle(t, s, 100*time.Millisecond, 10*time.Second)
	checkSet(t, s, appA, want, false)
}

// EDGE-85 with EDGE-84: a write timeout in the middle of a tail-bounded
// resend resumes it. Every line of the newest N arrives once, nothing older
// does, and live lines are not held back.
func TestTailResendResumesAfterWriteTimeout(t *testing.T) {
	old := sinkReconnectDelay
	sinkReconnectDelay = 20 * time.Millisecond
	defer func() { sinkReconnectDelay = old }()

	dir := t.TempDir()
	m := New("host1", dir, log15.New())
	now := time.Now()
	writeLogFile(t, filepath.Join(dir, appA+".log"), appA, seqLines(1, 3000, now.Add(-time.Minute), time.Millisecond), now.Add(-time.Minute))
	writeLogFile(t, filepath.Join(dir, appB+".log"), appB, seqLines(1, 3000, now.Add(-time.Minute), time.Millisecond), now.Add(-time.Minute))

	s := newAggSink()
	s.limits = resendLimits{Lines: 1000}
	s.delay = func(l aggLine) time.Duration {
		if l.app != appLive {
			return 500 * time.Microsecond
		}
		return 0
	}
	var mu sync.Mutex
	failed := false
	s.fail = func(s *aggSink, l aggLine) error {
		mu.Lock()
		defer mu.Unlock()
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
		mu.Lock()
		defer mu.Unlock()
		return failed
	})
	waitFor(t, 10*time.Second, "the resend", func() bool {
		return len(s.lines(appA)) >= 1000 && len(s.lines(appB)) >= 1000
	})
	close(stop)
	n := <-liveN
	// Live lines that overflowed the sink buffer are resent from the file
	// too, under the same limit: the newest 1000 must all be there.
	newestLive := rangeInts(1, n)
	if n > 1000 {
		newestLive = rangeInts(n-999, n)
	}
	waitFor(t, 10*time.Second, "the live lines", func() bool {
		got := make(map[string]bool)
		for _, l := range s.lines(appLive) {
			got[l.text] = true
		}
		for _, i := range newestLive {
			if !got[fmt.Sprintf("%s-%d", appLive, i)] {
				return false
			}
		}
		return true
	})
	settle(t, s, 100*time.Millisecond, 10*time.Second)
	m.waitIdle()

	s.mu.Lock()
	connects := s.connects
	s.mu.Unlock()
	if connects < 2 {
		t.Fatalf("connects=%d, the write timeout did not reconnect", connects)
	}
	checkSet(t, s, appA, rangeInts(2001, 3000), true)
	checkSet(t, s, appB, rangeInts(2001, 3000), true)
	seen := make(map[string]int)
	for _, l := range s.lines(appLive) {
		if seen[l.text]++; seen[l.text] == 2 {
			t.Errorf("live line %s arrived twice", l.text)
		}
	}
}

// The backward reader finds frame starts by their MSG-LEN, so a message
// that holds a newline is one frame, a partial line at the end (still being
// written) is skipped, and chunk edges do not matter.
func TestBackFrames(t *testing.T) {
	var b bytes.Buffer
	var want []string
	for i := 0; i < 5000; i++ {
		msg := fmt.Sprintf("line %d", i)
		if i%97 == 0 {
			msg = fmt.Sprintf("two\nrows %d", i)
		}
		if i%1013 == 0 {
			msg = strings.Repeat("x", rfc6587.MaxMsgLen)
		}
		want = append(want, msg)
		fmt.Fprintf(&b, "%d %s\n", len(msg), msg)
	}
	b.WriteString("12 partial")
	data := b.Bytes()
	br := newBackFrames(bytes.NewReader(data), int64(len(data)))
	for i := len(want) - 1; i >= 0; i-- {
		off, msg, err := br.prev()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if string(msg) != want[i] {
			t.Fatalf("frame %d at %d: %q, want %q", i, off, msg, want[i])
		}
	}
	if _, _, err := br.prev(); err != io.EOF {
		t.Fatalf("after the first frame: %v, want EOF", err)
	}

	// Garbage longer than any frame: the newer frames are still read.
	junk := append(bytes.Repeat([]byte("junk\n"), maxFrameLen/4), []byte("5 hello\n")...)
	br = newBackFrames(bytes.NewReader(junk), int64(len(junk)))
	if _, msg, err := br.prev(); err != nil || string(msg) != "hello" {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
	if _, _, err := br.prev(); err != errCorruptFrame {
		t.Fatalf("err=%v, want errCorruptFrame", err)
	}
}
