package logmux

import (
	"sort"
	"sync"
	"time"

	"github.com/randy-girard/flynn/logaggregator/utils"
)

// resendSlack is how far back, in log time, a resumed resend starts before
// the newest resent line the sink took. Lines of one app can sit slightly out
// of order in its file (several writers append to it), so the resend keeps the
// keys of the lines it wrote in this window and skips only those.
var resendSlack = 10 * time.Second

// pendingRange is part of one app's history a sink still lacks: the lines with
// a cursor after from (nil: from the first line) up to and including to.
type pendingRange struct {
	from *utils.HostCursor
	to   utils.HostCursor
	gen  uint64
}

func (r pendingRange) has(c utils.HostCursor) bool {
	return (r.from == nil || c.After(*r.from)) && !c.After(r.to)
}

type lineKey struct {
	usec int64
	seq  uint64
}

type sentLine struct {
	app    string
	cursor utils.HostCursor
}

// wireCursor is c at the precision of the syslog timestamp (microseconds),
// which is what the log files and the aggregator hold.
func wireCursor(c utils.HostCursor) utils.HostCursor {
	return utils.HostCursor{Time: c.Time.Truncate(time.Microsecond), Seq: c.Seq}
}

func keyOf(c utils.HostCursor) lineKey {
	return lineKey{c.Time.UnixMicro(), c.Seq}
}

// resendState is what one sink still needs from the host's log files. It
// lives as long as the sink, across reconnects. The sink's own cursor is the
// newest line it took, and live lines go out while the files are resent, so
// that cursor says nothing about the resend: a write timeout used to
// reconnect, take a cursor from a live line and skip the rest of the resend.
type resendState struct {
	mtx     sync.Mutex
	pending map[string][]pendingRange
	gen     uint64
	// sent holds the lines written inside a pending range (live lines, and
	// resent lines within resendSlack of the newest), so they are not
	// resent again.
	sent map[lineKey]sentLine
	// newest is, per app, the newest resent line written.
	newest map[string]utils.HostCursor
	// sinkCursor is the cursor the sink reported on the last connect.
	sinkCursor *utils.HostCursor
	wake       chan struct{}

	started time.Time
	lines   uint64
}

func newResendState() *resendState {
	return &resendState{
		pending: make(map[string][]pendingRange),
		sent:    make(map[lineKey]sentLine),
		newest:  make(map[string]utils.HostCursor),
		wake:    make(chan struct{}, 1),
	}
}

// connected records a new connection: the sink holds every line up to its
// cursor except the pending ranges, and lacks every line after it up to upTo
// (the newest line before the firehose subscription; later lines come live).
// A sink with no cursor, or with a cursor older than at the last connect (a
// new aggregator at the same address), gets the files from the start. It
// returns whether a resend was already under way.
func (r *resendState) connected(apps []string, cursor *utils.HostCursor, upTo utils.HostCursor) (resumed bool) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if cursor != nil {
		c := wireCursor(*cursor)
		cursor = &c
	}
	if cursor == nil || (r.sinkCursor != nil && r.sinkCursor.After(*cursor)) {
		r.pending = make(map[string][]pendingRange)
		r.sent = make(map[lineKey]sentLine)
		r.newest = make(map[string]utils.HostCursor)
	}
	resumed = len(r.pending) > 0
	if !resumed {
		r.started = time.Now()
		r.lines = 0
	}
	r.sinkCursor = cursor
	r.addLocked(apps, cursor, upTo)
	return resumed
}

// add marks the lines of apps in (from, to] as missing from the sink.
func (r *resendState) add(apps []string, from *utils.HostCursor, to utils.HostCursor) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if len(r.pending) == 0 {
		r.started = time.Now()
		r.lines = 0
	}
	r.addLocked(apps, from, to)
}

func (r *resendState) addLocked(apps []string, from *utils.HostCursor, to utils.HostCursor) {
	to = wireCursor(to)
	if from != nil {
		f := wireCursor(*from)
		from = &f
		if !to.After(*from) {
			return
		}
	}
	r.gen++
	for _, app := range apps {
		r.pending[app] = mergeRange(r.pending[app], pendingRange{from: from, to: to, gen: r.gen})
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// mergeRange adds n to the sorted, disjoint ranges rs.
func mergeRange(rs []pendingRange, n pendingRange) []pendingRange {
	all := append(append([]pendingRange(nil), rs...), n)
	sort.Slice(all, func(i, j int) bool { return cursorBefore(all[i].from, all[j].from) })
	out := all[:1]
	for _, x := range all[1:] {
		last := &out[len(out)-1]
		if x.from == nil || !x.from.After(last.to) {
			if x.to.After(last.to) {
				last.to = x.to
			}
			if x.gen > last.gen {
				last.gen = x.gen
			}
			continue
		}
		out = append(out, x)
	}
	return out
}

// cursorBefore orders range starts, nil (the first line) first.
func cursorBefore(a, b *utils.HostCursor) bool {
	if a == nil {
		return b != nil
	}
	return b != nil && b.After(*a)
}

// wants reports whether the line of app at c is still to be resent.
func (r *resendState) wants(app string, c utils.HostCursor) bool {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	for _, p := range r.pending[app] {
		if p.has(c) {
			_, done := r.sent[keyOf(c)]
			return !done
		}
	}
	return false
}

// wroteLive records a live line the sink took. One inside a pending range
// (it was in the firehose and also in the file) is not resent.
func (r *resendState) wroteLive(app string, c utils.HostCursor) {
	c = wireCursor(c)
	r.mtx.Lock()
	defer r.mtx.Unlock()
	for _, p := range r.pending[app] {
		if p.has(c) {
			r.sent[keyOf(c)] = sentLine{app, c}
			return
		}
	}
}

// wroteResent records a resent line the sink took and moves the start of the
// app's pending ranges up to resendSlack before the newest resent line.
func (r *resendState) wroteResent(app string, c utils.HostCursor) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.lines++
	r.sent[keyOf(c)] = sentLine{app, c}
	if n, ok := r.newest[app]; ok && !c.After(n) {
		return
	}
	r.newest[app] = c
	floor := utils.HostCursor{Time: c.Time.Add(-resendSlack)}
	rs := r.pending[app]
	i := 0
	for i < len(rs) && !rs[i].to.After(floor) {
		i++
	}
	rs = rs[i:]
	if len(rs) > 0 && (rs[0].from == nil || floor.After(*rs[0].from)) {
		f := floor
		rs[0].from = &f
	}
	if len(rs) == 0 {
		delete(r.pending, app)
	} else {
		r.pending[app] = rs
	}
	if r.lines%4096 == 0 {
		r.pruneLocked()
	}
}

// pruneLocked drops sent keys no pending range covers.
func (r *resendState) pruneLocked() {
	for k, s := range r.sent {
		keep := false
		for _, p := range r.pending[s.app] {
			if p.has(s.cursor) {
				keep = true
				break
			}
		}
		if !keep {
			delete(r.sent, k)
		}
	}
}

// snapshot returns the generation and, per app with a pending range, the
// earliest start (nil: the first line), for one pass over the files.
func (r *resendState) snapshot() (uint64, map[string]*utils.HostCursor) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	apps := make(map[string]*utils.HostCursor, len(r.pending))
	for app, rs := range r.pending {
		apps[app] = rs[0].from
	}
	return r.gen, apps
}

// passDone is called once every line a pass over the files queued has been
// written: the ranges that pass covered are done, whether or not their lines
// were still on disk. It reports whether nothing is left, with the number of
// lines resent and how long the resend took.
func (r *resendState) passDone(gen uint64) (finished bool, lines uint64, took time.Duration) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	for app, rs := range r.pending {
		keep := rs[:0]
		for _, p := range rs {
			if p.gen > gen {
				keep = append(keep, p)
			}
		}
		if len(keep) == 0 {
			delete(r.pending, app)
		} else {
			r.pending[app] = keep
		}
	}
	if len(r.pending) > 0 {
		r.pruneLocked()
		return false, 0, 0
	}
	r.sent = make(map[lineKey]sentLine)
	r.newest = make(map[string]utils.HostCursor)
	return true, r.lines, time.Since(r.started)
}
