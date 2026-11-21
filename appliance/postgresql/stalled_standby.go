package postgresql

import (
	"time"

	"github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// A standby can be up, consistent and answering queries, yet never replicate
// again. CORE-25-F1-F3: after a remaster during flynn-host update the sync's
// local WAL ended in a record with an incorrect prev-link past the new
// primary's timeline switch. Postgres retried "waiting for WAL to become
// available" every 5s for hours with no walreceiver, while the reused-standby
// health check had been skipped because the new primary's sirenia API did not
// answer at that moment. The checks in start() and verifyOrReseedStandby run
// once; this watchdog keeps watching for as long as the peer is a standby.

// standbyProgressTracker decides when a running standby is stuck. Progress is
// a streaming walreceiver or a replay LSN that moved. A standby only counts as
// stuck once it has made no progress for the whole budget and its upstream has
// answered as online for the whole budget too, so an upstream restart, a
// network partition or a slow local replay never triggers a reseed.
type standbyProgressTracker struct {
	timeout        time.Duration
	lastLSN        string
	progressAt     time.Time
	upstreamOKFrom time.Time
}

func newStandbyProgressTracker(timeout time.Duration) *standbyProgressTracker {
	return &standbyProgressTracker{timeout: timeout}
}

// observe records one sample and reports whether the standby is stuck.
// receiver is pg_stat_wal_receiver.status ("" when no walreceiver runs or the
// query failed), lsn is pg_last_wal_replay_lsn() ("" when unknown).
func (t *standbyProgressTracker) observe(now time.Time, receiver, lsn string, upstreamOK bool) bool {
	if t.progressAt.IsZero() {
		t.progressAt, t.lastLSN = now, lsn
	}
	if receiver == "streaming" || (lsn != "" && lsn != t.lastLSN) {
		t.progressAt = now
	}
	if lsn != "" {
		t.lastLSN = lsn
	}
	if !upstreamOK {
		t.upstreamOKFrom = time.Time{}
		return false
	}
	if t.upstreamOKFrom.IsZero() {
		t.upstreamOKFrom = now
	}
	return now.Sub(t.progressAt) >= t.timeout && now.Sub(t.upstreamOKFrom) >= t.timeout
}

// upstreamOnline reports whether a peer can serve a standby: its postgres runs
// and reports an xlog position, it is not copying a basebackup itself, and it
// is not a standby that has stalled. A stalled standby is consistent and
// answers queries, so the plain running/xlog/user check passed for one and
// every async copied a dead replica from it.
func upstreamOnline(status *client.Status, err error) (bool, string) {
	switch {
	case err != nil:
		return false, "unreachable: " + err.Error()
	case status == nil || status.Database == nil:
		return false, "no database status"
	case !status.Database.Running:
		return false, "postgres not running"
	case status.Database.Basebackup != nil:
		return false, "copying a basebackup"
	case status.Database.StandbyStalled:
		return false, "upstream standby is stalled (no WAL streamed or replayed)"
	case status.Database.XLog == "":
		return false, "no xlog position"
	}
	return true, ""
}

// sameStandbyTarget compares the role and upstream only. sirenia re-applies
// configs and swaps the downstream (async replaced) all the time; restarting
// the budget on each of those would let a stuck sync dodge the watchdog while
// the cluster churns around it, which is exactly when it got stuck.
func sameStandbyTarget(a, b *state.Config) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Role != b.Role || a.Upstream == nil || b.Upstream == nil {
		return false
	}
	return a.Upstream.Equal(b.Upstream)
}

func (p *Process) standbyStalled() bool { return p.stalledVal.Load() }

// startStandbyProgressWatch starts the watchdog once per process. It idles
// while the peer is not a running standby.
func (p *Process) startStandbyProgressWatch() {
	if !p.progressWatchStarted.CompareAndSwap(false, true) {
		return
	}
	go p.watchStandbyProgress()
}

// replicationSample reads the walreceiver status and replay LSN. A failed
// query (postgres back in "starting up" after a crash restart) counts as no
// progress.
func (p *Process) replicationSample() (receiver, lsn string) {
	p.dbMtx.RLock()
	defer p.dbMtx.RUnlock()
	if !p.running() || p.db == nil {
		return "", ""
	}
	_ = p.db.QueryRow(`SELECT COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), ''),
		COALESCE(pg_last_wal_replay_lsn()::text, '')`).Scan(&receiver, &lsn)
	return receiver, lsn
}

func (p *Process) watchStandbyProgress() {
	log := p.log.New("fn", "watchStandbyProgress")
	var (
		tracker *standbyProgressTracker
		cfg     *state.Config
		gen     uint64
	)
	for {
		time.Sleep(p.stallCheckInterval)
		cur := p.config()
		if p.stopped() || cur == nil || !standbyRole(cur.Role) || cur.Upstream == nil ||
			!p.running() || p.inStart.Load() || p.basebackup.Load() != nil {
			tracker = nil
			p.stalledVal.Store(false)
			continue
		}
		if tracker == nil || !sameStandbyTarget(cur, cfg) || p.startGen.Load() != gen {
			tracker, cfg, gen = newStandbyProgressTracker(p.stallTimeout), cur, p.startGen.Load()
		}
		receiver, lsn := p.replicationSample()
		ok, reason := upstreamOnline(p.upstreamStatus(cfg.Upstream.Addr))
		stuck := tracker.observe(time.Now(), receiver, lsn, ok)
		p.stalledVal.Store(stuck)
		if !stuck {
			if !ok && receiver != "streaming" {
				log.Debug("standby not streaming, upstream not online, not reseeding", "upstream", cfg.Upstream.Addr, "reason", reason)
			}
			continue
		}
		p.reseedStalledStandby(cfg, gen, receiver, lsn)
		tracker = nil
	}
}

// reseedStalledStandby replaces the data directory of a standby the watchdog
// found stuck. It runs under p.mtx and gives up if sirenia reconfigured the
// peer or postgres restarted since the samples were taken. The primary is
// never touched; pullBasebackup keeps the old data until the copy succeeds.
func (p *Process) reseedStalledStandby(cfg *state.Config, gen uint64, receiver, lsn string) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	log := p.log.New("fn", "reseedStalledStandby", "upstream", cfg.Upstream.Addr, "role", cfg.Role)
	if !sameStandbyTarget(p.config(), cfg) || p.startGen.Load() != gen || p.stopped() || !p.running() {
		log.Info("not reseeding stalled standby: reconfigured or restarted since it was sampled")
		p.stalledVal.Store(false)
		return
	}
	if receiver == "" {
		receiver = "none"
	}
	log.Warn("standby made no replication progress while its upstream was online, re-seeding from upstream",
		"wal_receiver", receiver, "replay_lsn", lsn, "budget", p.stallTimeout)
	if err := p.reseedStandbyFromUpstream(log, cfg.Upstream); err != nil {
		log.Error("reseed of stalled standby failed", "err", err)
		return
	}
	p.setNeedsReseed(false)
	p.setWALRecycled(false)
	p.stalledVal.Store(false)
	log.Info("stalled standby re-seeded from upstream")
}
