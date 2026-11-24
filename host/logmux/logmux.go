package logmux

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inconshreveable/log15"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/stream"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
	"github.com/randy-girard/flynn/pkg/syslog/rfc6587"
	"gopkg.in/natefinch/lumberjack.v2"
)

type message struct {
	*utils.HostCursor
	*rfc5424.Message
}

// LogMux collects log lines from multiple leaders and forwards them to
// logaggregator instances and local files.
type Mux struct {
	hostID string
	logDir string
	logger log15.Logger

	msgSeq uint32
	// drops counts lines that did not fit in a sink buffer or the on-disk
	// queue. Follower timeouts are not included. The host status handler
	// reads this when a status request arrives. It is not a controller write.
	drops uint64

	jobsMtx sync.Mutex
	// jobWaits stores WaitGroups for jobs that we're currently following
	jobWaits map[string]*sync.WaitGroup
	// jobStarts stores a channel to signal followers waiting for us to start
	// following a job, in order to wait on the appropriate group from jobWaits,
	// as a WaitGroup can't be waited until the counter is >0
	jobStarts map[string]chan struct{}

	subscribersMtx sync.RWMutex
	subscribers    map[string]map[chan message]*sinkSub

	appLogsMtx sync.Mutex
	appLogs    map[string]*appLog
	// diskWG counts in-flight log file writers. Tests wait for it before
	// removing the temp log directory; production callers do not.
	diskWG sync.WaitGroup

	// diskQueued and diskWritten count lines put on, and taken off, the
	// disk queues (written or failed), so a resend can wait for the files
	// to catch up.
	diskQueued, diskWritten uint64

	// diskWriter, if set, replaces lumberjack so tests can stall disk I/O.
	diskWriter func(path string) io.WriteCloser
}

const firehoseApp = "_all"

func New(hostID, logDir string, logger log15.Logger) *Mux {
	return &Mux{
		hostID:      hostID,
		logDir:      logDir,
		logger:      logger,
		jobWaits:    make(map[string]*sync.WaitGroup),
		jobStarts:   make(map[string]chan struct{}),
		subscribers: make(map[string]map[chan message]*sinkSub),
		appLogs:     make(map[string]*appLog),
	}
}

type Config struct {
	AppID, HostID, JobID, JobType, JobName string
}

// procID is syslog PROCID: processType.hostJobID so filters can recover the
// real host job UUID. The allocated short name (web.1) is not used here.
func (c *Config) procID() []byte {
	if c.JobType != "" {
		return []byte(c.JobType + "." + c.JobID)
	}
	return []byte(c.JobID)
}

// sinkSub records, for one firehose subscriber (a sink), the lines that did
// not fit in its channel, so the sink can resend them from the log files.
type sinkSub struct {
	mtx sync.Mutex
	// queued is the newest line put in the channel.
	queued *utils.HostCursor
	// dropped is set by a drop; dropFrom is then the newest line queued
	// before the first drop since the last take.
	dropped  bool
	dropFrom *utils.HostCursor
}

func (s *sinkSub) sent(c *utils.HostCursor) {
	s.mtx.Lock()
	s.queued = c
	s.mtx.Unlock()
}

func (s *sinkSub) drop() {
	s.mtx.Lock()
	if !s.dropped {
		s.dropped = true
		s.dropFrom = s.queued
	}
	s.mtx.Unlock()
}

// take reports whether lines were dropped since the last take, and the newest
// line queued before the first of them (nil if none was).
func (s *sinkSub) take() (bool, *utils.HostCursor) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	d, from := s.dropped, s.dropFrom
	s.dropped, s.dropFrom = false, nil
	return d, from
}

func (m *Mux) subscribe(app string, ch chan message) func() {
	unsubscribe, _ := m.subscribeSink(app, ch)
	return unsubscribe
}

func (m *Mux) subscribeSink(app string, ch chan message) (func(), *sinkSub) {
	m.subscribersMtx.Lock()
	defer m.subscribersMtx.Unlock()
	subs, ok := m.subscribers[app]
	if !ok {
		subs = make(map[chan message]*sinkSub)
		m.subscribers[app] = subs
	}
	sub := &sinkSub{}
	subs[ch] = sub
	return func() {
		go func() {
			// drain channel to prevent deadlock
			for range ch {
			}
		}()
		m.subscribersMtx.Lock()
		defer m.subscribersMtx.Unlock()
		delete(m.subscribers[app], ch)
	}, sub
}

// broadcastTimeout is the one deadline shared by every per-app follower in a
// single broadcast. A stalled `flynn log -f` must not stack another second
// per follower, and it must not hang the host. Sink subscribers do not use
// this timer.
var broadcastTimeout = time.Second

// sinkReconnectDelay is the wait after a sink connect or write error before
// the next dial. A bad prefix template returns from Write; without this wait
// that error reconnects in a tight loop on every host that has the sink.
var sinkReconnectDelay = 10 * time.Second

// Drops is the number of job log lines this process has dropped because a
// sink buffer or the on-disk queue was full. It is in-memory and resets when
// the process exits.
func (m *Mux) Drops() uint64 {
	if m == nil {
		return 0
	}
	return atomic.LoadUint64(&m.drops)
}

// StoreDropsForTest sets the counter so host status tests can read a known
// value without filling a sink buffer.
func (m *Mux) StoreDropsForTest(n uint64) {
	if m == nil {
		return
	}
	atomic.StoreUint64(&m.drops, n)
}

func (m *Mux) addDrop() {
	atomic.AddUint64(&m.drops, 1)
}

func (m *Mux) broadcast(app string, msg message) {
	m.subscribersMtx.RLock()
	defer m.subscribersMtx.RUnlock()

	l := m.logger.New("fn", "broadcast", "sample", "0.1")
	r := rand.New(rand.NewSource(time.Now().Unix()))
	// Firehose subscribers are sinks. A full buffer drops the line. Waiting
	// here holds the job stdout reader, and one timeout per sink stacks.
	// The sink is told, and resends the dropped lines from the log file.
	for ch, sub := range m.subscribers[firehoseApp] {
		select {
		case ch <- msg:
			sub.sent(msg.HostCursor)
		default:
			m.addDrop()
			sub.drop()
			if r.Intn(9) == 0 {
				l.Error("dropping log line due to full sink buffer")
			}
		}
	}
	// Per-app followers used to get an unbounded send here. subscribe()
	// drains the channel on unsubscribe, but a live subscriber that is itself
	// blocked forwarding to a slow HTTP client never reads, and everything
	// upstream of this call (appLog.Write, State.sendEvent under s.mtx) hung
	// with it. One deadline covers every follower of this line. A follower
	// timeout is not a sink drop.
	timeout := time.NewTimer(broadcastTimeout)
	defer timeout.Stop()
	deadlineHit := false
	for ch := range m.subscribers[app] {
		if deadlineHit {
			select {
			case ch <- msg:
			default:
				if r.Intn(9) == 0 {
					l.Error("dropping log line due to follower write timeout", "app", app)
				}
			}
			continue
		}
		select {
		case ch <- msg:
		default:
			select {
			case ch <- msg:
			case <-timeout.C:
				deadlineHit = true
				if r.Intn(9) == 0 {
					l.Error("dropping log line due to follower write timeout", "app", app)
				}
			}
		}
	}
}

// resendDiskWait bounds how long a pass over the log files waits for lines
// already queued for disk to be written, so a line logged just before the
// sink subscribed is in the file when the pass reads it.
var resendDiskWait = 2 * time.Second

func (m *Mux) addSink(sink Sink) {
	l := m.logger.New("fn", "addSink", "name", sink.Name())
	shutdownCh := sink.ShutdownCh()
	reconnectDelay := 0 * time.Second
	// What the sink still lacks from the log files. It outlives a
	// connection so a resend cut by a write error resumes where it stopped.
	resend := newResendState()

	for {
		select {
		case <-shutdownCh:
			sink.Close()
			return
		case <-time.After(reconnectDelay):
			failed := m.sendToSink(sink, resend, l)
			select {
			case <-shutdownCh:
				sink.Close()
				return
			default:
			}
			if failed {
				reconnectDelay = sinkReconnectDelay
			} else {
				reconnectDelay = 0
			}
		}
	}
}

// resendItem is a line from a log file for the sink writer, or, with passEnd
// set, the end of one pass over the files (acked once handled).
type resendItem struct {
	msg     message
	app     string
	size    int
	passEnd bool
	gen     uint64
	ack     chan struct{}
}

// sendToSink connects the sink and sends it the live firehose and, from the
// log files, what it lacks, until a write fails (it returns true: reconnect
// after the delay) or the sink is shut down.
//
// Live lines go first. A resend from the files (a sink with no cursor gets
// every file) runs beside them: one line at a time when no live line waits.
// Live lines used to be held (dropped, to come later from the file) until
// their app's files were resent, which could be minutes.
func (m *Mux) sendToSink(sink Sink, resend *resendState, l log15.Logger) (failed bool) {
	shutdownCh := sink.ShutdownCh()
	l.Info("connecting to sink")
	if err := sink.Connect(); err != nil {
		l.Error("error connecting to sink", "err", err)
		return true
	}
	defer sink.Close()
	l.Info("connected to sink")
	sinkCursor, err := sink.GetCursor(m.hostID)
	if err != nil {
		l.Error("failed to get cursor from sink", "err", err)
	}
	if sinkCursor != nil {
		l.Info("got cursor", "cursor.timestamp", sinkCursor.Time, "cursor.seq", sinkCursor.Seq)
	} else {
		l.Info("no cursor for host")
	}
	// A logaggregator keeps only part of each app's history; it is resent
	// no more than that (EDGE-85).
	limiter, _ := sink.(resendLimiter)
	if limiter != nil {
		lim := limiter.resendLimits()
		l.Info("resend limits", "lines", lim.Lines, "max_age", lim.MaxAge)
	}

	// Buffer the live firehose so a slow sink write does not drop the
	// first lines. When this fills, broadcast drops and counts the line,
	// and the sink resends it from the file.
	firehose := make(chan message, 256)
	unsubscribe, sub := m.subscribeSink(firehoseApp, firehose)
	defer unsubscribe()
	// Every line up to here is in the files (or on its way to them); every
	// later one comes through the firehose.
	upTo := m.cursorNow()

	apps, err := m.logApps()
	if err != nil {
		l.Error("failed to get local log files", "error", err)
		return true
	}
	if resend.connected(apps, sinkCursor, upTo) {
		l.Info("resuming resend of log files")
	}

	resent := make(chan resendItem)
	done := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		m.readResend(resend, limiter, resent, done, l)
	}()
	defer func() {
		close(done)
		<-readerDone
	}()

	lastLive := upTo
	for {
		var item resendItem
		live := false
		select {
		case item.msg = <-firehose:
			live = true
		default:
			select {
			case <-shutdownCh:
				return false
			case item.msg = <-firehose:
				live = true
			case item = <-resent:
			}
		}

		// Lines that did not fit in the firehose are resent from the
		// files. This is checked before a live line is written so a
		// line queued after the drop counts as sent.
		if dropped, from := sub.take(); dropped {
			if from == nil {
				c := lastLive
				from = &c
			}
			if apps, err := m.logApps(); err == nil {
				resend.add(apps, from, m.cursorNow())
			}
		}

		switch {
		case live:
			if err := sink.Write(item.msg); err != nil {
				l.Error("failed to write message to sink", "error", err)
				return true
			}
			resend.wroteLive(string(item.msg.Message.AppName), *item.msg.HostCursor)
			if item.msg.HostCursor.After(lastLive) {
				lastLive = *item.msg.HostCursor
			}
		case item.passEnd:
			if finished, n, took := resend.passDone(item.gen); finished {
				l.Info("resend of log files finished", "lines", n, "bytes", resend.bytesSent(), "duration", took)
			}
			close(item.ack)
		default:
			if err := sink.Write(item.msg); err != nil {
				l.Error("failed to write message to sink", "error", err, "resend", true)
				return true
			}
			resend.wroteResent(item.app, *item.msg.HostCursor)
			resend.addBytes(item.size)
		}
	}
}

// cursorNow is a cursor no older than any line logged so far.
func (m *Mux) cursorNow() utils.HostCursor {
	return utils.HostCursor{Time: time.Now(), Seq: uint64(atomic.LoadUint32(&m.msgSeq))}
}

// logApps lists the app ids that have log files.
func (m *Mux) logApps() ([]string, error) {
	files, err := m.resendLogFiles()
	if err != nil {
		return nil, err
	}
	apps := make([]string, 0, len(files))
	for app := range files {
		apps = append(apps, app)
	}
	return apps, nil
}

// knownAppsTimeout bounds the controller app list a pass waits for, and
// knownAppsMaxAge is how long one list serves later passes.
var (
	knownAppsTimeout = 10 * time.Second
	knownAppsMaxAge  = time.Minute
)

// knownAppsGrace: an app missing from the controller's list is skipped only
// if its files were last written this long before the list was fetched, so
// an app created since (or one deleted a moment ago, whose late lines the
// aggregator refuses itself) is not lost.
const knownAppsGrace = time.Minute

// passPlan is what one pass over the files may skip.
type passPlan struct {
	limits  resendLimits
	now     time.Time
	known   map[string]bool // nil: not known, skip no app as deleted
	knownAt time.Time
}

// skip reports why an app whose newest file was written at newest is not
// read ("" to read it).
func (p *passPlan) skip(app string, newest time.Time) string {
	if p.limits.MaxAge > 0 && newest.Before(p.now.Add(-p.limits.MaxAge)) {
		return "expired"
	}
	if p.known != nil && !p.known[app] && newest.Before(p.knownAt.Add(-knownAppsGrace)) {
		return "deleted"
	}
	return ""
}

// readResend sends the sink writer the lines of the log files that resend
// still wants, a pass at a time, until done is closed. A new pass starts when
// lines are added (a reconnect, or live lines dropped from the firehose).
// With a limiter (a logaggregator sink) each app is read from the end of its
// files and only as far back as the aggregator keeps, and apps the
// aggregator would not keep are skipped.
func (m *Mux) readResend(resend *resendState, limiter resendLimiter, out chan<- resendItem, done <-chan struct{}, l log15.Logger) {
	var known map[string]bool
	var knownAt time.Time
	for {
		select {
		case <-resend.wake:
		default:
		}
		gen, apps := resend.snapshot()
		if len(apps) > 0 {
			m.waitDisk(resendDiskWait, done)
			plan := passPlan{now: time.Now()}
			if limiter != nil {
				plan.limits = limiter.resendLimits()
				if known == nil || time.Since(knownAt) > knownAppsMaxAge {
					at := time.Now()
					k, err := fetchKnownApps(limiter, knownAppsTimeout, done)
					if err != nil {
						l.Info("app list unavailable, no app is skipped as deleted", "error", err)
						known = nil
					} else {
						known, knownAt = k, at
					}
				}
				plan.known, plan.knownAt = known, knownAt
			}
			files, err := m.resendLogFiles()
			if err != nil {
				l.Error("failed to get local log files", "error", err)
			}
			if limiter == nil {
				// A syslog sink gets what it always got: the .log files.
				for app, names := range files {
					files[app] = currentLogFiles(names)
				}
			}
			ids := make([]string, 0, len(apps))
			for app := range apps {
				ids = append(ids, app)
			}
			sort.Strings(ids)
			var read, expired, deleted int
			full := false // a range from the first line: a cursorless resend
			for _, app := range ids {
				if apps[app] == nil {
					full = true
				}
				fs := statLogFiles(files[app])
				if len(fs) == 0 {
					continue
				}
				switch plan.skip(app, fs[len(fs)-1].mtime) {
				case "expired":
					expired++
					continue
				case "deleted":
					deleted++
					continue
				}
				read++
				if !m.resendApp(resend, app, fs, apps[app], plan.limits, out, done, l) {
					return
				}
			}
			if limiter != nil && (full || expired+deleted > 0) {
				l.Info("resend pass", "apps", len(ids), "read", read, "skipped_expired", expired, "skipped_deleted", deleted, "known_apps", len(known))
			}
			ack := make(chan struct{})
			select {
			case out <- resendItem{passEnd: true, gen: gen, ack: ack}:
			case <-done:
				return
			}
			select {
			case <-ack:
			case <-done:
				return
			}
		}
		select {
		case <-resend.wake:
		case <-done:
			return
		}
	}
}

// fetchKnownApps asks the limiter for the controller's app ids, waiting at
// most timeout.
func fetchKnownApps(limiter resendLimiter, timeout time.Duration, done <-chan struct{}) (map[string]bool, error) {
	type result struct {
		known map[string]bool
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		k, err := limiter.knownApps()
		ch <- result{k, err}
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.known, r.err
	case <-t.C:
		return nil, fmt.Errorf("no app list after %s", timeout)
	case <-done:
		return nil, errors.New("resend stopped")
	}
}

// logFileInfo is one log file of an app.
type logFileInfo struct {
	name  string
	mtime time.Time
}

// statLogFiles returns the files that exist, oldest write first. A file is
// never written after its last line's time, so mtime orders them and bounds
// their lines.
func statLogFiles(names []string) []logFileInfo {
	res := make([]logFileInfo, 0, len(names))
	for _, name := range names {
		fi, err := os.Stat(name)
		if err != nil || fi.IsDir() {
			continue
		}
		res = append(res, logFileInfo{name, fi.ModTime()})
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].mtime.Before(res[j].mtime) })
	return res
}

// resendLogFileName matches the files the resend reads for an app: the
// current file, lumberjack's backups (<id>-<time>.log) and logrotate's
// uncompressed copy of either (.log.1). After logrotate's daily copytruncate
// the current file holds only the lines since, so the aggregator's ring is
// refilled from the copy as well. Compressed copies are not read.
var resendLogFileName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(-[^/]*)?\.log(\.1)?$`)

// resendLogFiles returns, per app id, the log files a resend reads.
func (m *Mux) resendLogFiles() (map[string][]string, error) {
	files, err := ioutil.ReadDir(m.logDir)
	if err != nil {
		return nil, err
	}
	res := make(map[string][]string)
	for _, f := range files {
		n := f.Name()
		if f.IsDir() || !resendLogFileName.MatchString(n) {
			continue
		}
		res[n[:36]] = append(res[n[:36]], filepath.Join(m.logDir, n))
	}
	return res, nil
}

// currentLogFiles drops logrotate's copies from names.
func currentLogFiles(names []string) []string {
	res := names[:0]
	for _, n := range names {
		if strings.HasSuffix(n, ".log") {
			res = append(res, n)
		}
	}
	return res
}

// resendApp sends the wanted lines of one app's files (oldest first). With
// no line limit and no range start every line is read from the start, as
// for a syslog sink with no cursor. Otherwise the files are read from the
// end, back to the oldest line the limit or the range start can still want.
// It returns false once done is closed.
func (m *Mux) resendApp(resend *resendState, app string, files []logFileInfo, from *utils.HostCursor, limits resendLimits, out chan<- resendItem, done <-chan struct{}, l log15.Logger) bool {
	if limits.Lines <= 0 && from == nil {
		for _, f := range files {
			if !m.resendFile(resend, app, f.name, nil, out, done, l) {
				return false
			}
		}
		return true
	}
	return m.resendTail(resend, app, files, from, limits.Lines, out, done, l)
}

// tailLine is a line found going backward: its file, offset and time.
type tailLine struct {
	file int
	off  int64
	at   time.Time
}

// resendTail sends, of the lines of files after from (nil: any), the limit
// newest by time (limit <= 0: all), oldest first. Lines of one app sit out
// of time order in its files by less than resendSlack, so the backward read
// stops once a line is that much older than the oldest line still kept.
func (m *Mux) resendTail(resend *resendState, app string, files []logFileInfo, from *utils.HostCursor, limit int, out chan<- resendItem, done <-chan struct{}, l log15.Logger) bool {
	keep := newest{n: limit}
	var lines []tailLine
	var floor time.Time
	if from != nil {
		floor = from.Time.Add(-resendSlack)
	}
	n := 0
	for i := len(files) - 1; i >= 0; i-- {
		fi := files[i]
		if from != nil && fi.mtime.Before(from.Time) {
			continue // nothing in it is after from
		}
		if keep.full() && fi.mtime.Before(keep.oldest().Add(-resendSlack)) {
			continue // nothing in it is new enough
		}
		ll := l.New("log", fi.name)
		f, err := openResendFile(fi.name)
		if err != nil {
			ll.Error("failed to open log file", "error", err)
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			ll.Error("failed to stat log file", "error", err)
			continue
		}
		br := newBackFrames(f, st.Size())
		for {
			off, msgBytes, err := br.prev()
			if err == io.EOF {
				break
			}
			if err != nil {
				ll.Error("failed to read log file backward, older lines are not resent", "error", err, "offset", br.end)
				break
			}
			if n++; n%4096 == 0 {
				select {
				case <-done:
					f.Close()
					return false
				default:
				}
			}
			_, cursor, err := utils.ParseMessage(msgBytes)
			if err != nil {
				continue
			}
			at := cursor.Time
			if from != nil && at.Before(floor) {
				break
			}
			if keep.full() && at.Before(keep.oldest().Add(-resendSlack)) {
				break
			}
			keep.offer(at)
			lines = append(lines, tailLine{i, off, at})
		}
		f.Close()
	}
	cutoff := keep.oldest()
	// lines is newest file first, backward in each: send it reversed.
	for j := len(lines) - 1; j >= 0; {
		i := lines[j].file
		var offs []int64
		for ; j >= 0 && lines[j].file == i; j-- {
			if !lines[j].at.Before(cutoff) {
				offs = append(offs, lines[j].off)
			}
		}
		if len(offs) > 0 && !m.resendOffsets(resend, app, files[i].name, offs, out, done, l) {
			return false
		}
	}
	return true
}

// resendOffsets sends the wanted lines of one file that start at offs
// (ascending), reading forward from the first.
func (m *Mux) resendOffsets(resend *resendState, app, name string, offs []int64, out chan<- resendItem, done <-chan struct{}, l log15.Logger) bool {
	l = l.New("log", name)
	f, err := openResendFile(name)
	if err != nil {
		l.Error("failed to open log file", "error", err)
		return true
	}
	defer f.Close()
	pos := offs[0]
	var tokOff int64
	split := func(data []byte, atEOF bool) (int, []byte, error) {
		adv, tok, err := rfc6587.SplitWithNewlines(data, atEOF)
		if tok != nil {
			tokOff = pos
			pos += int64(adv)
		}
		return adv, tok, err
	}
	sc := bufio.NewScanner(io.NewSectionReader(f, offs[0], 1<<62))
	sc.Buffer(make([]byte, 0, tailChunk), maxFrameLen+1)
	sc.Split(split)
	k := 0
	for k < len(offs) && sc.Scan() {
		if tokOff < offs[k] {
			continue
		}
		for k < len(offs) && offs[k] < tokOff {
			k++ // not a frame start going forward: skip it
		}
		if k == len(offs) || offs[k] != tokOff {
			continue
		}
		k++
		msgBytes := sc.Bytes()
		msgCopy := make([]byte, len(msgBytes)-1)
		copy(msgCopy, msgBytes)
		msg, cursor, err := utils.ParseMessage(msgCopy)
		if err != nil {
			continue
		}
		if !resend.wants(app, *cursor) {
			continue
		}
		size := len(msgCopy) + len(strconv.Itoa(len(msgCopy))) + 2
		select {
		case out <- resendItem{msg: message{cursor, msg}, app: app, size: size}:
		case <-done:
			return false
		}
	}
	if err := sc.Err(); err != nil {
		l.Error("failed to scan message", "error", err)
	}
	return true
}

// resendFile sends the wanted lines of one log file. A file last written
// before from holds nothing wanted and is not read. It returns false once
// done is closed.
func (m *Mux) resendFile(resend *resendState, app, name string, from *utils.HostCursor, out chan<- resendItem, done <-chan struct{}, l log15.Logger) bool {
	l = l.New("log", name)
	f, err := openResendFile(name)
	if err != nil {
		l.Error("failed to open log file", "error", err)
		return true
	}
	defer f.Close()
	if from != nil {
		if fi, err := f.Stat(); err == nil && fi.ModTime().Before(from.Time) {
			return true
		}
	}
	sc := bufio.NewScanner(io.NewSectionReader(f, 0, 1<<62))
	sc.Split(rfc6587.SplitWithNewlines)
	n := 0
	for sc.Scan() {
		if n++; n%4096 == 0 {
			select {
			case <-done:
				return false
			default:
			}
		}
		msgBytes := sc.Bytes()
		// slice in msgBytes could get modified on next Scan(), need to copy it
		msgCopy := make([]byte, len(msgBytes)-1)
		copy(msgCopy, msgBytes)
		msg, cursor, err := utils.ParseMessage(msgCopy)
		if err != nil {
			l.Error("failed to parse message", "msg", string(msgCopy), "error", err)
			continue
		}
		if !resend.wants(app, *cursor) {
			continue
		}
		size := len(msgCopy) + len(strconv.Itoa(len(msgCopy))) + 2
		select {
		case out <- resendItem{msg: message{cursor, msg}, app: app, size: size}:
		case <-done:
			return false
		}
	}
	if err := sc.Err(); err != nil {
		l.Error("failed to scan message", "error", err)
	}
	return true
}

// resendFile is a log file the resend reads. Tests count its reads.
type resendFile interface {
	io.ReaderAt
	io.Reader
	io.Closer
	Stat() (os.FileInfo, error)
}

var openResendFile = func(name string) (resendFile, error) { return os.Open(name) }

// waitDisk waits, at most d, until the lines queued for disk so far are
// written.
func (m *Mux) waitDisk(d time.Duration, done <-chan struct{}) {
	target := atomic.LoadUint64(&m.diskQueued)
	deadline := time.Now().Add(d)
	for atomic.LoadUint64(&m.diskWritten) < target && time.Now().Before(deadline) {
		select {
		case <-done:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

type MuxLogger struct {
	log15.Logger
	*LogStream
}

func (m *Mux) Logger(msgID logagg.MsgID, config *Config, ctx ...interface{}) *MuxLogger {
	r, w := io.Pipe()
	logger := log15.New(ctx...)
	logger.SetHandler(log15.StreamHandler(w, log15.LogfmtFormat()))
	stream := m.Follow(r, "", msgID, config)
	return &MuxLogger{logger, stream}
}

// Follow starts a goroutine that reads log lines from the reader into the mux.
// It runs until the reader is closed or an error occurs. If an error occurs,
// the reader may still be open.
func (m *Mux) Follow(r io.ReadCloser, buffer string, msgID logagg.MsgID, config *Config) *LogStream {
	hdr := &rfc5424.Header{
		Hostname: []byte(config.HostID),
		AppName:  []byte(config.AppID),
		MsgID:    []byte(msgID),
		Severity: 6,  // Info
		Facility: 23, // local7
	}
	hdr.ProcID = config.procID()

	s := &LogStream{
		m:    m,
		log:  r,
		done: make(chan struct{}),
	}
	s.closed.Store(false)

	m.jobsMtx.Lock()
	defer m.jobsMtx.Unlock()
	// set up the WaitGroup so that subscribers can track when the job fds have closed
	wg, ok := m.jobWaits[config.JobID]
	if !ok {
		wg = &sync.WaitGroup{}
		m.jobWaits[config.JobID] = wg
	}
	wg.Add(1)
	if !ok {
		// we created the wg, so create a goroutine to clean up
		go func() {
			wg.Wait()
			m.jobsMtx.Lock()
			defer m.jobsMtx.Unlock()
			delete(m.jobWaits, config.JobID)
		}()
	}

	// if there is a jobStart channel, a subscriber is waiting for the WaitGroup
	// to be created, signal it.
	if ch, ok := m.jobStarts[config.JobID]; ok {
		close(ch)
		delete(m.jobStarts, config.JobID)
	}

	go s.follow(r, buffer, config.AppID, hdr, config.JobName, wg)
	return s
}

// Write emits a single log line for an app (used for Flynn lifecycle messages).
// It does not track job file descriptors; callers must not rely on it to keep
// a job's WaitGroup open.
func (m *Mux) Write(msgID logagg.MsgID, config *Config, line string) {
	if m == nil || config == nil {
		return
	}
	line = strings.TrimRight(line, "\n")
	if line == "" || strings.TrimSpace(config.AppID) == "" {
		return
	}
	hdr := &rfc5424.Header{
		Hostname: []byte(config.HostID),
		AppName:  []byte(config.AppID),
		MsgID:    []byte(msgID),
		Severity: 6,
		Facility: 23,
	}
	hdr.ProcID = config.procID()
	msg := rfc5424.NewMessage(hdr, []byte(line))
	cursor := &utils.HostCursor{
		Time: msg.Timestamp,
		Seq:  uint64(atomic.AddUint32(&m.msgSeq, 1)),
	}
	sd := flynnStructuredData(cursor.Seq, config.JobName)
	var sdBuf bytes.Buffer
	sd.Encode(&sdBuf)
	msg.StructuredData = sdBuf.Bytes()
	l := m.appLog(config.AppID)
	l.Write(message{cursor, msg})
	l.Release()
}

type LogStream struct {
	m      *Mux
	log    io.Closer
	buf    string
	closed atomic.Value // bool
	done   chan struct{}
}

func (s *LogStream) Close() string {
	s.closed.Store(true)
	s.log.Close()
	<-s.done
	return s.buf
}

// CloseTimeout closes the stream like Close but waits at most d for its
// follow goroutine to return. ok is false when it did not; the goroutine is
// then left running and its partial line is lost.
func (s *LogStream) CloseTimeout(d time.Duration) (buf string, ok bool) {
	s.closed.Store(true)
	s.log.Close()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.done:
		return s.buf, true
	case <-t.C:
		return "", false
	}
}

func (s *LogStream) follow(r io.Reader, buffer, appID string, h *rfc5424.Header, jobName string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer close(s.done)
	l := s.m.appLog(appID)
	defer l.Release()
	seqBuf := make([]byte, 10)
	sd := flynnStructuredData(0, jobName)

	br := bufio.NewReaderSize(io.MultiReader(strings.NewReader(buffer), r), 10000)
	for {
		line, err := br.ReadSlice('\n')
		if err != nil && err != bufio.ErrBufferFull {
			// if the log was explicitly closed (because an update
			// is in progress), store the buffer and return so it
			// can be passed to the new flynn-host daemon.
			if s.closed.Load().(bool) {
				s.buf = string(line)
				return
			}
			if len(line) == 0 {
				// only return if there is no final line to return
				return
			}
		}
		if line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}

		hdr := *h
		if logagg.MsgID(h.MsgID) != logagg.MsgIDInit && utils.IsPluginMetricsLine(line) {
			hdr.MsgID = []byte(logagg.MsgIDSystem)
		}
		msg := rfc5424.NewMessage(&hdr, line)
		cursor := &utils.HostCursor{
			Time: msg.Timestamp,
			Seq:  uint64(atomic.AddUint32(&s.m.msgSeq, 1)),
		}
		sd.Params[0].Value = strconv.AppendUint(seqBuf[:0], cursor.Seq, 10)
		var sdBuf bytes.Buffer
		sd.Encode(&sdBuf)
		msg.StructuredData = sdBuf.Bytes()
		l.Write(message{cursor, msg})

		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}

func flynnStructuredData(seq uint64, jobName string) *rfc5424.StructuredData {
	params := []rfc5424.StructuredDataParam{
		{Name: []byte("seq"), Value: []byte(strconv.FormatUint(seq, 10))},
	}
	if jobName != "" {
		params = append(params, rfc5424.StructuredDataParam{
			Name:  []byte("job_name"),
			Value: []byte(jobName),
		})
	}
	return &rfc5424.StructuredData{ID: []byte("flynn"), Params: params}
}

func (m *Mux) StreamLog(appID, jobID string, history, follow bool, ch chan<- *rfc5424.Message) (stream.Stream, error) {
	if history {
		return m.streamWithHistory(appID, jobID, follow, ch)
	}
	return m.followLog(appID, jobID, ch)
}

// jobDoneCh returns a channel that is closed when all of the streams we are
// following from the job have been closed. It will never unblock if the job has
// already finished.
func (m *Mux) jobDoneCh(jobID string, stop <-chan struct{}) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)
		var started chan struct{}
		m.jobsMtx.Lock()
		// check if there is already a WaitGroup
		wg, ok := m.jobWaits[jobID]
		if !ok {
			// if not, check if there is a channel to be notified when one is
			// created
			started, ok = m.jobStarts[jobID]
			if !ok {
				// if not, make and save the channel
				started = make(chan struct{})
				m.jobStarts[jobID] = started
			}
		}
		m.jobsMtx.Unlock()

		if started != nil {
			// if the wg doesn't exist, wait for it
			select {
			case <-started:
			case <-stop:
				return
			}
			m.jobsMtx.Lock()
			wg, ok = m.jobWaits[jobID]
			m.jobsMtx.Unlock()
			if !ok {
				// if there is no wg, it was created and deleted before we could
				// read it, we're done
				return
			}
		}

		// wait for the job to finish
		wg.Wait()
	}()
	return ch
}

func (m *Mux) followLog(appID, jobID string, ch chan<- *rfc5424.Message) (stream.Stream, error) {
	s := stream.New()
	var jobDone <-chan struct{}
	if jobID != "" {
		jobDone = m.jobDoneCh(jobID, s.StopCh)
	}
	go func() {
		msgs := make(chan message)
		unsubscribe := m.subscribe(appID, msgs)
		defer unsubscribe()
		defer close(ch)
		for {
			select {
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				if jobID != "" && !strings.HasSuffix(string(msg.Message.Header.ProcID), jobID) {
					// skip messages that aren't from the job we care about
					continue
				}
				select {
				case ch <- msg.Message:
				case <-s.StopCh:
					return
				}
			case <-s.StopCh:
				return
			case <-jobDone:
				return
			}
		}
	}()
	return s, nil
}

func (m *Mux) streamWithHistory(appID, jobID string, follow bool, ch chan<- *rfc5424.Message) (stream.Stream, error) {
	l := m.logger.New("fn", "streamWithHistory", "app.id", appID, "job.id", jobID)
	logs, err := m.logFiles(appID)
	if err != nil {
		return nil, err
	}
	if len(logs) == 0 {
		return m.followLog(appID, jobID, ch)
	}

	msgs := make(chan message)
	unsubscribeFn := make(chan func(), 1)

	s := stream.New()
	var jobDone <-chan struct{}
	if jobID != "" {
		jobDone = m.jobDoneCh(jobID, s.StopCh)
	}

	go func() {
		var cursor *utils.HostCursor
		var unsubscribe func()
		var done bool
		defer func() {
			close(ch)
			if unsubscribe != nil {
				unsubscribe()
			}
		}()
		for {
			select {
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				if jobID != "" && !strings.HasSuffix(string(msg.Message.Header.ProcID), jobID) {
					// skip messages that aren't from the job we care about
					continue
				}
				if cursor != nil && !msg.HostCursor.After(*cursor) {
					// skip messages with old cursors
					continue
				}
				cursor = msg.HostCursor
				select {
				case ch <- msg.Message:
				case <-s.StopCh:
					return
				}
			case <-jobDone:
				if unsubscribe != nil {
					return
				}
				// we haven't finished reading the historical logs, exit when finished
				done = true
				jobDone = nil
			case fn, ok := <-unsubscribeFn:
				if !ok {
					if done {
						// historical logs done, and job already exited
						return
					}
					unsubscribeFn = nil
					continue
				}
				unsubscribe = fn
			case <-s.StopCh:
				return
			}
		}
	}()

	go func() {
		defer close(unsubscribeFn)
		for i, name := range logs[appID] {
			if err := func() (err error) {
				l := l.New("log", name)
				f, err := os.Open(name)
				if err != nil {
					l.Error("error reading log", "error", err)
					return err
				}
				defer f.Close()
				sc := bufio.NewScanner(f)
				sc.Split(rfc6587.SplitWithNewlines)
				var eof bool
			scan:
				for sc.Scan() {
					msgBytes := sc.Bytes()
					// slice in msgBytes could get modified on next Scan(), need to copy it
					msgCopy := make([]byte, len(msgBytes)-1)
					copy(msgCopy, msgBytes)
					msg, cursor, err := utils.ParseMessage(msgCopy)
					if err != nil {
						l.Error("error parsing log message", "error", err)
						return err
					}
					select {
					case msgs <- message{cursor, msg}:
					case <-s.StopCh:
						return nil
					}
				}
				if err := sc.Err(); err != nil {
					l.Error("error scanning log message", "error", err)
					return err
				}
				if follow && !eof && i == len(logs[appID])-1 {
					// got EOF on last file, subscribe to stream
					eof = true
					unsubscribeFn <- m.subscribe(appID, msgs)
					// read to end of file again
					goto scan
				}
				return nil
			}(); err != nil {
				close(msgs)
				s.Error = err
				return
			}
		}
		if !follow {
			close(msgs)
		}
	}()

	return s, nil
}

var appIDPrefixPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// logFiles returns a list of app IDs and the list of log file names associated
// with them, from oldest to newest. There is always at least one file, the
// current log for the app.
func (m *Mux) logFiles(app string) (map[string][]string, error) {
	files, err := ioutil.ReadDir(m.logDir)
	if err != nil {
		return nil, err
	}

	res := make(map[string][]string)
	for _, f := range files {
		n := f.Name()
		if f.IsDir() || !strings.HasSuffix(n, ".log") || !appIDPrefixPattern.MatchString(n) || !strings.HasPrefix(n, app) {
			continue
		}
		appID := n[:36]
		res[appID] = append(res[appID], filepath.Join(m.logDir, n))
	}

	return res, nil
}

func (m *Mux) appLog(id string) *appLog {
	m.appLogsMtx.Lock()
	defer m.appLogsMtx.Unlock()

	// if existing, return it and increment reference count
	if l, ok := m.appLogs[id]; ok {
		l.mtx.Lock()
		defer l.mtx.Unlock()
		if l.refs > 0 {
			l.refs++
			return l
		}
		// if refs == 0, it was closed before we got it, create a new one
	}

	// if not, create log
	path := filepath.Join(m.logDir, id+".log")
	var w io.WriteCloser
	if m.diskWriter != nil {
		w = m.diskWriter(path)
	} else {
		// MaxSize is megabytes. lumberjack treats a megabyte as 1024×1024,
		// so this file plus MaxBackups 1 is about 200MB for one app on this
		// host. It is not a cluster total. Do not raise it to pretend the
		// ring is retention.
		w = &lumberjack.Logger{
			Filename:   path,
			MaxSize:    100,
			MaxBackups: 1,
		}
	}
	l := &appLog{
		appID:  id,
		m:      m,
		refs:   1,
		diskCh: make(chan []byte, appLogDiskQueue),
		w:      w,
	}
	m.diskWG.Add(1)
	go l.writeDisk()
	m.appLogs[id] = l
	return l
}

func (m *Mux) waitIdle() {
	m.diskWG.Wait()
}

// appLogDiskQueue is how many lines Follow may have in flight to disk. A
// stuck lumberjack write (vboxsf, NFS) must not back up job stdout: the
// controller logs every HTTP request, and a full stdout pipe wedges /ping.
const appLogDiskQueue = 1024

type appLog struct {
	appID  string
	m      *Mux
	w      io.WriteCloser
	diskCh chan []byte

	mtx  sync.Mutex
	refs int
}

func (l *appLog) writeDisk() {
	defer l.m.diskWG.Done()
	defer l.w.Close()
	for b := range l.diskCh {
		if _, err := l.w.Write(b); err != nil {
			l.m.logger.Error("error writing job log", "app.id", l.appID, "err", err)
		}
		atomic.AddUint64(&l.m.diskWritten, 1)
	}
}

func (l *appLog) Write(msg message) {
	payload := append(rfc6587.Bytes(msg.Message), '\n')
	cp := make([]byte, len(payload))
	copy(cp, payload)
	select {
	case l.diskCh <- cp:
		atomic.AddUint64(&l.m.diskQueued, 1)
	default:
		l.m.addDrop()
		if rand.Intn(50) == 0 {
			l.m.logger.Error("dropping job log line, disk queue full", "app.id", l.appID)
		}
	}
	l.m.broadcast(l.appID, msg)
}

// Release releases the app log, when the last job releases an app log, it is
// closed.
//
// Lock order is appLogsMtx, then l.mtx, the same as Mux.appLog. Taking l.mtx
// first deadlocked a lifecycle line (Mux.Write) racing the last log stream of
// the same app: from then on every Follow on the host hung, and so did the
// job cleanup waiting on LogStream.Close.
func (l *appLog) Release() {
	l.m.appLogsMtx.Lock()
	defer l.m.appLogsMtx.Unlock()
	l.mtx.Lock()
	defer l.mtx.Unlock()
	l.refs--
	if l.refs == 0 {
		// Drain the disk writer instead of closing w here: writeDisk may
		// still be blocked on lumberjack/vboxsf. Closing the queue lets it
		// exit and Close the file when the in-flight write returns.
		close(l.diskCh)
		if l.m.appLogs[l.appID] == l {
			delete(l.m.appLogs, l.appID)
		}
	}
}
