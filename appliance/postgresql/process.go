package postgresql

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"text/template"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/appliance/postgresql/pgxlog"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
	"github.com/randy-girard/flynn/pkg/sirenia/xlog"
)

const (
	IDKey = "POSTGRES_ID"

	// sireniaIDMarker is written beside PG_VERSION so a peer only reuses a
	// data directory that it previously initialized for this appliance id.
	sireniaIDMarker = "flynn-sirenia-id"
)

// Overridable for tests so standby replication health checks finish quickly.
var (
	standbyReplicationHealthTimeout = 30 * time.Second
	standbyReplicationCheckInterval = 500 * time.Millisecond
	// After a crash-reload the walreceiver can report "streaming" for a
	// second, then FATAL: requested WAL segment has already been removed.
	// Two samples are not enough; require it to stay up this long.
	standbyReplicationStableFor = 5 * time.Second
	// A reused standby whose restart LSN is gone hangs in "starting up" /
	// "waiting for WAL to become available" instead of FATAling. Do not
	// burn opTimeout (5m) before reseeding.
	standbyReuseStartupTimeout = 45 * time.Second
)

type Config struct {
	ID        string
	Singleton bool
	Port      string
	BinDir    string
	DataDir   string
	Password  string
	OpTimeout time.Duration
	// StopTimeout bounds each shutdown signal (SIGINT then SIGQUIT then
	// SIGKILL). Keep this under the host container WaitStop (30s) so a
	// job stop finishes a fast checkpoint instead of being SIGKILL'd and
	// forcing crash recovery on the next start.
	StopTimeout  time.Duration
	ReplTimeout  time.Duration
	Logger       log15.Logger
	TimescaleDB  bool
	ExtWhitelist bool
	SHMType      string
	WaitUpstream bool
	TLSHosts     []string
}

type Process struct {
	// dbMtx is locked while db is being changed
	dbMtx sync.RWMutex
	db    *pgx.ConnPool

	events chan state.DatabaseEvent

	configVal           atomic.Value // *state.Config
	runningVal          atomic.Value // bool
	syncedDownstreamVal atomic.Value // *discoverd.Instance
	configApplied       bool

	// config options
	id           string
	log          log15.Logger
	singleton    bool
	port         string
	binDir       string
	dataDir      string
	password     string
	opTimeout    time.Duration
	stopTimeout  time.Duration
	replTimeout  time.Duration
	timescaleDB  bool
	extWhitelist bool
	shmType      string
	waitUpstream bool
	tlsHosts     []string

	// daemon is the postgres daemon command when running
	daemon *exec.Cmd
	// expectExit is bool, true if the daemon is supposed to exit
	expectExit atomic.Value
	// daemonExit is closed when the daemon exits
	daemonExit chan struct{}
	// walRecycledVal is true when postgres stderr reported a recycled WAL
	// segment (FATAL: requested WAL segment ... has already been removed).
	walRecycledVal atomic.Value
	// needsReseedVal is true after a WAL-recycle FATAL so the next standby
	// start reseeds from the upstream instead of replaying a dead replica.
	needsReseedVal atomic.Value
	// justBasebackup is set after a successful pg_basebackup so the following
	// start uses opTimeout. A reused datadir (no fresh backup) uses the short
	// hang budget instead.
	justBasebackup bool
	// basebackup is the latest pg_basebackup progress while one is running.
	basebackup atomic.Pointer[client.BasebackupProgress]
	// inStart is true while start() is waiting for postgres to accept
	// connections. The WAL detector reseeds itself only after start has
	// returned; during start the wait loop owns the reseed.
	inStart atomic.Bool
	// reseedScheduled keeps a single reseed goroutine when WAL recycle is
	// logged after start() already returned success.
	reseedScheduled atomic.Bool

	// cancelSyncWait cancels the goroutine that is waiting for the downstream to
	// catch up, if running
	cancelSyncWait func()
	// cancelUpstreamWait cancels waitForUpstream so /stop is not blocked
	// behind a 90s poll of a hung replica.
	cancelUpstreamWait func()
	upstreamWaitMtx    sync.Mutex
	stoppedVal         atomic.Value // bool

	// mtx ensures that only one operation happens at a time
	mtx sync.Mutex
}

const checkInterval = 100 * time.Millisecond

func NewProcess(c Config) *Process {
	p := &Process{
		id:                 c.ID,
		log:                c.Logger,
		singleton:          c.Singleton,
		port:               c.Port,
		binDir:             c.BinDir,
		dataDir:            c.DataDir,
		password:           c.Password,
		opTimeout:          c.OpTimeout,
		stopTimeout:        c.StopTimeout,
		replTimeout:        c.ReplTimeout,
		timescaleDB:        c.TimescaleDB,
		extWhitelist:       c.ExtWhitelist,
		shmType:            c.SHMType,
		waitUpstream:       c.WaitUpstream,
		tlsHosts:           c.TLSHosts,
		events:             make(chan state.DatabaseEvent, 1),
		cancelSyncWait:     func() {},
		cancelUpstreamWait: func() {},
	}
	p.setRunning(false)
	p.setConfig(nil)
	p.setSyncedDownstream(nil)
	p.setWALRecycled(false)
	p.setNeedsReseed(false)
	p.setStopped(false)
	if p.log == nil {
		p.log = log15.New("app", "postgres", "id", p.id)
	}
	if p.port == "" {
		p.port = "5432"
	}
	if p.binDir == "" {
		p.binDir = "/usr/lib/postgresql/16/bin/"
	}
	if p.dataDir == "" {
		p.dataDir = "/data"
	}
	if p.password == "" {
		p.password = "password"
	}
	if p.opTimeout == 0 {
		p.opTimeout = 5 * time.Minute
	}
	if p.stopTimeout == 0 {
		p.stopTimeout = 10 * time.Second
	}
	if p.replTimeout == 0 {
		p.replTimeout = 1 * time.Minute
	}
	p.events <- state.DatabaseEvent{}
	return p
}

func (p *Process) XLog() xlog.XLog {
	return pgxlog.PgXLog{}
}

func (p *Process) running() bool {
	return p.runningVal.Load().(bool)
}

func (p *Process) setRunning(running bool) {
	p.runningVal.Store(running)
}

func (p *Process) config() *state.Config {
	return p.configVal.Load().(*state.Config)
}

func (p *Process) setConfig(config *state.Config) {
	p.configVal.Store(config)
}

func (p *Process) syncedDownstream() *discoverd.Instance {
	return p.syncedDownstreamVal.Load().(*discoverd.Instance)
}

func (p *Process) setSyncedDownstream(inst *discoverd.Instance) {
	p.syncedDownstreamVal.Store(inst)
}

func (p *Process) walRecycled() bool {
	v := p.walRecycledVal.Load()
	if v == nil {
		return false
	}
	return v.(bool)
}

func (p *Process) setWALRecycled(v bool) {
	p.walRecycledVal.Store(v)
}

func (p *Process) needsReseed() bool {
	v := p.needsReseedVal.Load()
	if v == nil {
		return false
	}
	return v.(bool)
}

func (p *Process) setNeedsReseed(v bool) {
	p.needsReseedVal.Store(v)
}

func (p *Process) stopped() bool {
	v := p.stoppedVal.Load()
	if v == nil {
		return false
	}
	return v.(bool)
}

func (p *Process) setStopped(v bool) {
	p.stoppedVal.Store(v)
}

func (p *Process) Info() (*client.DatabaseInfo, error) {
	res := &client.DatabaseInfo{
		Config:           p.config(),
		Running:          p.running(),
		SyncedDownstream: p.syncedDownstream(),
		Basebackup:       p.basebackup.Load(),
	}
	xlog, err := p.XLogPosition()
	res.XLog = string(xlog)
	if err != nil {
		return res, err
	}
	res.UserExists, err = p.userExists()
	if err != nil {
		return res, err
	}
	res.ReadWrite, err = p.isReadWrite()
	if err != nil {
		return res, err
	}
	return res, err
}

func (p *Process) Reconfigure(config *state.Config) error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	switch config.Role {
	case state.RolePrimary:
		// Nil downstream is one-node-write: singleton, or SINGLETON=false at
		// scale 1 after the scheduler env-flips a 1-node cluster toward HA
		// before replicas exist. Rejecting that leaves postgres stopped,
		// controller cannot PutFormation, and HA scale deadlocks.
		// assumePrimary already starts RW with a nil downstream.
	case state.RoleSync, state.RoleAsync:
		if config.Upstream == nil {
			return fmt.Errorf("missing upstream peer")
		}
	case state.RoleNone:
	default:
		return fmt.Errorf("unknown role %v", config.Role)
	}

	if !p.running() {
		p.setConfig(config)
		p.configApplied = false
		return nil
	}

	return p.reconfigure(config)
}

func (p *Process) Start() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	p.setStopped(false)

	if p.running() {
		return errors.New("postgres is already running")
	}
	if p.config() == nil {
		return errors.New("postgres is unconfigured")
	}
	if p.config().Role == state.RoleNone {
		return errors.New("start attempted with role 'none'")
	}

	return p.reconfigure(nil)
}

func (p *Process) Stop() error {
	p.setStopped(true)
	p.CancelUpstreamWait()
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if !p.running() {
		return nil
	}
	return p.stop()
}

func (p *Process) XLogPosition() (xlog.Position, error) {
	p.dbMtx.RLock()
	defer p.dbMtx.RUnlock()

	if !p.running() || p.db == nil {
		return "", errors.New("postgres is not running")
	}

	fn := "pg_last_wal_replay_lsn()"
	if p.config().Role == state.RolePrimary {
		fn = "pg_current_wal_lsn()"
	}
	var res string
	err := p.db.QueryRow("SELECT " + fn).Scan(&res)
	return xlog.Position(res), err
}

func (p *Process) userExists() (bool, error) {
	p.dbMtx.RLock()
	defer p.dbMtx.RUnlock()

	if !p.running() || p.db == nil {
		return false, errors.New("postgres is not running")
	}
	var res pgx.NullInt32
	err := p.db.QueryRow("SELECT 1 FROM pg_roles WHERE rolname='flynn'").Scan(&res)
	return res.Valid, err
}

func (p *Process) isReadWrite() (bool, error) {
	p.dbMtx.RLock()
	defer p.dbMtx.RUnlock()

	if !p.running() || p.db == nil {
		return false, errors.New("postgres is not running")
	}
	var res string
	err := p.db.QueryRow("SHOW default_transaction_read_only").Scan(&res)
	return res == "off", err
}

func (p *Process) Ready() <-chan state.DatabaseEvent {
	return p.events
}

func (p *Process) Running() bool {
	return p.running()
}

func (p *Process) reconfigure(config *state.Config) (err error) {
	log := p.log.New("fn", "reconfigure")

	defer func() {
		if err == nil {
			p.setConfig(config)
			p.configApplied = true
		}
	}()

	if config != nil && config.Role == state.RoleNone {
		log.Info("nothing to do", "reason", "null role")
		return nil
	}

	// If we've already applied the same postgres config, we don't need to do anything
	if p.configApplied && config != nil && p.config() != nil && config.Equal(p.config()) {
		log.Info("nothing to do", "reason", "config already applied")
		return nil
	}

	// Promoting async to sync with the same upstream does not require a
	// restart, but downstream sync tracking must be started for sirenia deploys.
	if p.configApplied && p.running() && p.config() != nil && config != nil &&
		p.config().Role == state.RoleAsync && config.Role == state.RoleSync && config.Upstream.Meta[IDKey] == p.config().Upstream.Meta[IDKey] {
		log.Info("promoting async to sync with same upstream")
		p.cancelSyncWait()
		p.setSyncedDownstream(nil)
		if config.Downstream != nil {
			log.Info("waiting for downstream after sync promotion", "downstream", config.Downstream.Addr)
			p.waitForSync(config.Downstream, false)
		}
		return nil
	}

	// Make sure that we don't keep waiting for replication sync while reconfiguring
	p.cancelSyncWait()
	p.setSyncedDownstream(nil)

	// If we're already running and this is only a sync change, we just need to update the config.
	if p.running() && p.config() != nil && config != nil && p.config().Role == state.RolePrimary && config.Role == state.RolePrimary {
		return p.updateSync(config.Downstream)
	}

	// If we're already running and this is only a downstream change, just wait for the new downstream to catch up
	if p.running() && p.config().IsNewDownstream(config) {
		log.Info("downstream changed", "to", config.Downstream.Addr)
		p.waitForSync(config.Downstream, false)
		return
	}

	if config == nil {
		config = p.config()
	}

	if config.Role == state.RolePrimary {
		return p.assumePrimary(config.Downstream)
	}

	return p.assumeStandby(config.Upstream, config.Downstream)
}

func (p *Process) assumePrimary(downstream *discoverd.Instance) (err error) {
	log := p.log.New("fn", "assumePrimary")
	if downstream != nil {
		log = log.New("downstream", downstream.Addr)
	}

	if p.running() && p.config().Role == state.RoleSync {
		log.Info("promoting to primary")

		// PostgreSQL 16 removed promote_trigger_file, use pg_ctl promote instead
		if err := p.promote(); err != nil {
			log.Error("error promoting to primary", "err", err)
			return err
		}

		p.waitForSync(downstream, true)

		return nil
	}

	log.Info("starting as primary")

	if p.running() {
		panic(fmt.Sprintf("unexpected state running role=%s", p.config().Role))
	}

	if err := p.initDB(); err != nil {
		return err
	}

	if err := p.removeStandbySignal(); err != nil {
		log.Error("error removing standby.signal", "path", p.standbySignalPath(), "err", err)
		return err
	}

	if err := p.writeConfig(configData{ReadOnly: downstream != nil}); err != nil {
		log.Error("error writing postgres.conf", "path", p.configPath(), "err", err)
		return err
	}
	if err := p.writeSireniaIDMarker(); err != nil {
		log.Error("error writing sirenia id marker", "err", err)
		return err
	}

	if err := p.start(); err != nil {
		return err
	}

	var tx *pgx.Tx
	defer func() {
		if err != nil {
			if tx != nil {
				tx.Rollback()
			}
			p.db.Close()
			if err := p.stop(); err != nil {
				log.Debug("ignoring error stopping postgres", "err", err)
			}
		}
	}()

	tx, err = p.db.Begin()
	if err != nil {
		log.Error("error acquiring connection", "err", err)
		return err
	}
	if _, err := tx.Exec("SET TRANSACTION READ WRITE"); err != nil {
		log.Error("error setting transaction read-write", "err", err)
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`
		DO
		$body$
		BEGIN
		   IF NOT EXISTS (
			  SELECT * FROM pg_catalog.pg_user
			  WHERE	usename = 'flynn')
		   THEN
			  CREATE USER flynn WITH SUPERUSER CREATEDB CREATEROLE REPLICATION PASSWORD '%s';
		   END IF;
		END
		$body$;
	`, p.password)); err != nil {
		log.Error("error creating superuser", "err", err)
		return err
	}
	if err := tx.Commit(); err != nil {
		log.Error("error committing transaction", "err", err)
		return err
	}

	if downstream != nil {
		p.waitForSync(downstream, true)
	}

	return nil
}

func (p *Process) assumeStandby(upstream, downstream *discoverd.Instance) error {
	log := p.log.New("fn", "assumeStandby", "upstream", upstream.Addr)
	log.Info("starting up as standby")

	// TODO(titanous): investigate using a DNS name, proxy, or iptables rule for
	// the upstream. (perhaps DNS plus some postgres magic like terminating
	// a backend would work). Postgres appears to support remastering without
	// restarting:
	// http://www.databasesoup.com/2014/05/remastering-without-restarting.html

	reused := false
	if p.running() {
		// if we are running, we can just restart with new replication settings,
		// postgres supports streaming remastering.
		if err := p.stop(); err != nil {
			return err
		}
		reused = p.dataDirInitialized()
	} else if p.dataDirInitialized() {
		// The data directory already holds a copy of the database for this
		// peer id, so we can start locally in standby mode without the
		// upstream being reachable. This is essential when the upstream
		// primary has died: waiting for a dead upstream here would block the
		// database from ever starting, which in turn prevents a sync peer
		// from starting its takeover (startTakeoverWithPeer requires the
		// database to be running to read its xlog position). Postgres will
		// retry primary_conninfo until the upstream (or its replacement) is
		// available.
		log.Info("data directory already initialized, skipping basebackup and upstream wait")
		reused = true
	} else {
		if p.waitUpstream {
			// Do not hold mtx while polling a possibly hung upstream: /stop
			// (sirenia deploy) needs the lock, and a 90s waitForUpstream
			// deadlocked postgres.3787 so DeployAppRelease timed out.
			p.mtx.Unlock()
			err := p.waitForUpstream(upstream)
			p.mtx.Lock()
			if err != nil {
				return err
			}
			if p.stopped() {
				return errors.New("stopped")
			}
		}
		if err := p.pullBasebackup(log, upstream); err != nil {
			return err
		}
	}

	// Write postgresql.conf with replication settings (PostgreSQL 12+ style)
	cfg := configData{
		ReadOnly: true,
		PrimaryConnInfo: fmt.Sprintf(
			"host=%s port=%s user=flynn password=%s application_name=%s",
			upstream.Host(), upstream.Port(), p.password, p.id,
		),
		RecoveryTargetTimeline: "latest",
	}
	if err := p.writeConfig(cfg); err != nil {
		log.Error("error writing postgres.conf", "path", p.configPath(), "err", err)
		return err
	}

	// Create standby.signal file (PostgreSQL 12+ style instead of recovery.conf)
	if err := p.createStandbySignal(); err != nil {
		log.Error("error creating standby.signal", "path", p.standbySignalPath(), "err", err)
		return err
	}
	if err := p.writeSireniaIDMarker(); err != nil {
		log.Error("error writing sirenia id marker", "err", err)
		return err
	}

	if err := p.start(); err != nil {
		if p.stopped() {
			return err
		}
		if startErrorNeedsReseed(reused, p.walRecycled(), p.needsReseed()) {
			log.Warn("postgres died on reused data dir after WAL recycle, re-seeding from upstream", "err", err)
			if err := p.reseedStandbyFromUpstream(log, upstream); err != nil {
				return err
			}
			p.setNeedsReseed(false)
			p.setWALRecycled(false)
			if downstream != nil {
				p.waitForSync(downstream, false)
			}
			return nil
		}
		return err
	}

	if reused {
		if err := p.verifyOrReseedStandby(log, upstream); err != nil {
			return err
		}
	}

	if downstream != nil {
		p.waitForSync(downstream, false)
	}

	return nil
}

// pullBasebackup copies the upstream into a staging directory and only
// replaces the data directory once pg_basebackup succeeds. Clearing first
// destroyed every standby's only copy when a rolling deploy stopped the
// upstream mid-reseed.
func (p *Process) pullBasebackup(log log15.Logger, upstream *discoverd.Instance) error {
	staging := p.basebackupStagingDir()
	if err := os.RemoveAll(staging); err != nil {
		log.Error("error removing stale basebackup staging directory", "path", staging, "err", err)
		return err
	}
	if err := p.mkBasebackupStagingDir(staging); err != nil {
		log.Error("error creating basebackup staging directory", "path", staging, "err", err)
		return err
	}

	log.Info("pulling basebackup", "staging", staging)
	p.basebackup.Store(&client.BasebackupProgress{UpdatedAt: time.Now().UTC()})
	defer p.basebackup.Store(nil)
	cmd := exec.Command(
		p.binPath("pg_basebackup"),
		"--pgdata", staging,
		"--dbname", fmt.Sprintf(
			"host=%s port=%s user=flynn password=%s application_name=%s",
			upstream.Host(), upstream.Port(), p.password, upstream.Meta[IDKey],
		),
		"--wal-method=stream",
		// Force an immediate checkpoint on the upstream instead of
		// waiting (up to checkpoint_timeout) for the next spread
		// checkpoint. On a busy primary the default can add over a
		// minute of idle waiting, which pushes the initial sync past
		// the deploy's replication-sync timeout.
		"--checkpoint=fast",
		"--progress",
		"--verbose",
	)
	cmd.Stderr = io.MultiWriter(os.Stderr, &basebackupProgressWriter{dst: &p.basebackup})
	err := p.runCmd(cmd)
	if err != nil {
		log.Error("error pulling basebackup", "err", err)
		if rmErr := os.RemoveAll(staging); rmErr != nil {
			log.Error("error removing basebackup staging directory", "path", staging, "err", rmErr)
		}
		return err
	}
	if err := p.replaceDataDir(staging); err != nil {
		log.Error("error installing basebackup into data directory", "err", err)
		return err
	}
	// the upstream could be performing a takeover, so we need to
	// remove the trigger file if we have synced it across so we
	// don't also start a takeover.
	os.Remove(p.triggerPath())
	// The next start() is a fresh copy, not a reused datadir that may be
	// missing WAL. A short hang budget here would wipe a slow-but-valid backup.
	p.justBasebackup = true
	return nil
}

func (p *Process) verifyOrReseedStandby(log log15.Logger, upstream *discoverd.Instance) error {
	status, err := client.NewClient(upstream.Addr).Status()
	if skipStandbyHealthWhenUpstreamDown(status, err) {
		log.Warn("upstream unreachable, skipping standby replication health check", "err", err)
		return nil
	}
	if p.needsReseed() || p.walRecycled() {
		log.Warn("standby needs reseed after requested WAL segment was removed on upstream")
		if err := p.reseedStandbyFromUpstream(log, upstream); err != nil {
			return err
		}
		p.setNeedsReseed(false)
		p.setWALRecycled(false)
		return nil
	}
	ok, err := p.standbyReplicationHealthy()
	reseed, err := verifyStandbyReuseResult(p.running(), p.walRecycled() || p.needsReseed(), ok, err)
	if err != nil {
		return err
	}
	if !reseed {
		return nil
	}
	log.Warn("standby replication unhealthy after reusing initialized data dir, re-seeding from upstream")
	if err := p.reseedStandbyFromUpstream(log, upstream); err != nil {
		return err
	}
	p.setNeedsReseed(false)
	p.setWALRecycled(false)
	return nil
}

func (p *Process) reseedStandbyFromUpstream(log log15.Logger, upstream *discoverd.Instance) error {
	if err := p.stop(); err != nil {
		return err
	}
	if err := p.pullBasebackup(log, upstream); err != nil {
		// pullBasebackup leaves the existing datadir untouched on failure.
		// Restart on it so a sync can still take over when its upstream is
		// the peer that just went away.
		if p.dataDirInitialized() && !p.stopped() {
			if startErr := p.start(); startErr != nil {
				log.Error("error restarting on existing data after failed reseed", "err", startErr)
			} else {
				log.Warn("reseed failed, restarted on existing data directory", "err", err)
			}
		}
		return err
	}
	cfg := configData{
		ReadOnly: true,
		PrimaryConnInfo: fmt.Sprintf(
			"host=%s port=%s user=flynn password=%s application_name=%s",
			upstream.Host(), upstream.Port(), p.password, p.id,
		),
		RecoveryTargetTimeline: "latest",
	}
	if err := p.writeConfig(cfg); err != nil {
		return err
	}
	if err := p.createStandbySignal(); err != nil {
		return err
	}
	if err := p.writeSireniaIDMarker(); err != nil {
		return err
	}
	return p.start()
}

func (p *Process) reseedAfterWALRecycle() {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	cfg := p.config()
	if !standbyWALRecycleShouldReseed(cfg, p.stopped(), p.walRecycled() || p.needsReseed()) {
		return
	}
	log := p.log.New("fn", "reseedAfterWALRecycle")
	log.Warn("reseeding standby after requested WAL segment was removed on upstream")
	// start() can return success, then the walreceiver hits a missing timeline
	// and postgres stays up but stops accepting connections ("starting up").
	// Leaving running==true used to skip this reseed entirely.
	if p.running() {
		if err := p.stop(); err != nil {
			log.Error("error stopping postgres before WAL reseed", "err", err)
			return
		}
	}
	if err := p.reseedStandbyFromUpstream(log, cfg.Upstream); err != nil {
		log.Error("reseed after WAL recycle failed", "err", err)
		return
	}
	p.setNeedsReseed(false)
	p.setWALRecycled(false)
}

// standbyWALRecycleShouldReseed is true for a live sync or async whose
// datadir cannot replay (recycled WAL or a timeline the primary no longer
// has). A running postmaster is not a reason to skip: the hang leaves it up.
func standbyWALRecycleShouldReseed(cfg *state.Config, stopped, walRecycled bool) bool {
	if stopped || !walRecycled || cfg == nil || cfg.Upstream == nil {
		return false
	}
	return cfg.Role == state.RoleSync || cfg.Role == state.RoleAsync
}

// scheduleReseedAfterWALRecycle starts one reseed when postgres logs a
// recycled WAL after start() has already returned. During start(), the wait
// loop reseeds itself; scheduling here would deadlock on p.mtx.
func (p *Process) scheduleReseedAfterWALRecycle() {
	if p.inStart.Load() || p.stopped() {
		return
	}
	if !standbyWALRecycleShouldReseed(p.config(), false, true) {
		return
	}
	if !p.reseedScheduled.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer p.reseedScheduled.Store(false)
		p.reseedAfterWALRecycle()
	}()
}

// standbyReplicationHealthy reports whether the local standby is streaming or
// advancing WAL replay. Being behind the upstream is fine; a timeline mismatch
// or dead receiver that never progresses is not and triggers a reseed.
func (p *Process) standbyReplicationHealthy() (bool, error) {
	p.dbMtx.RLock()
	defer p.dbMtx.RUnlock()
	if !p.running() || p.db == nil {
		// Not an error: the caller reseeds. Returning an error here used to
		// skip reseed after a WAL-recycle FATAL during the health window.
		return false, nil
	}

	var startLSN string
	_ = p.db.QueryRow("SELECT COALESCE(pg_last_wal_replay_lsn()::text, '')").Scan(&startLSN)

	deadline := time.Now().Add(standbyReplicationHealthTimeout)
	consecutiveStreaming := 0
	var healthySince time.Time
	for time.Now().Before(deadline) {
		if !p.running() || p.walRecycled() {
			return false, nil
		}
		// pg_stat_wal_receiver has no error column; a recycled-WAL FATAL is
		// detected from the postgres log and surfaces via walRecycled().
		var status *string
		err := p.db.QueryRow("SELECT status FROM pg_stat_wal_receiver").Scan(&status)
		receiver := ""
		if err == nil && status != nil {
			receiver = *status
		}
		var lsn string
		if err := p.db.QueryRow("SELECT COALESCE(pg_last_wal_replay_lsn()::text, '')").Scan(&lsn); err != nil {
			lsn = ""
		}
		if receiver == "streaming" {
			consecutiveStreaming++
		} else {
			consecutiveStreaming = 0
		}
		live := postgresStandbyStreamingConfirmed(consecutiveStreaming) ||
			postgresStandbyLooksHealthy(receiver, startLSN, lsn)
		if live {
			if healthySince.IsZero() {
				healthySince = time.Now()
			}
			if time.Since(healthySince) >= standbyReplicationStableFor && p.running() && !p.walRecycled() {
				return true, nil
			}
		} else {
			healthySince = time.Time{}
		}
		if startLSN == "" && lsn != "" {
			startLSN = lsn
		}
		time.Sleep(standbyReplicationCheckInterval)
	}
	return false, nil
}

// skipStandbyHealthWhenUpstreamDown is true when verifyOrReseedStandby cannot
// compare against a live primary. Unlike MariaDB async peers, postgres skips
// rather than scheduling a deferred reseed — takeover still needs the local
// database running.
func skipStandbyHealthWhenUpstreamDown(status *client.Status, err error) bool {
	return err != nil || status == nil || status.Database == nil || !status.Database.Running
}

// postgresStandbyLooksHealthy reports advancing WAL replay through a live
// walreceiver. Local replay without streaming can look like progress after a
// crash, then FATAL when the next segment is already gone on the primary.
func postgresStandbyLooksHealthy(receiverStatus, startLSN, currentLSN string) bool {
	if receiverStatus != "streaming" {
		return false
	}
	return startLSN != "" && currentLSN != "" && currentLSN != startLSN
}

// postgresStandbyStreamingConfirmed is true after two consecutive "streaming"
// samples so a fleeting status cannot skip reseed, while an idle but live
// receiver (LSN not moving) still counts as healthy. The caller must still
// wait standbyReplicationStableFor so a recycled-WAL FATAL can surface.
func postgresStandbyStreamingConfirmed(consecutive int) bool {
	return consecutive >= 2
}

// postgresWALSegmentRemoved is true for the postgres FATAL that leaves a
// replica dead after the primary recycled WAL (or moved timeline) while this
// peer was down:
//
//	could not receive data from WAL stream: ERROR:  requested WAL segment
//	0000001E000000050000000B has already been removed
func postgresWALSegmentRemoved(msg string) bool {
	s := strings.ToLower(msg)
	if strings.Contains(s, "already been removed") {
		return true
	}
	if strings.Contains(s, "requested wal segment") && strings.Contains(s, "removed") {
		return true
	}
	// "waiting for WAL to become available" is deliberately not matched:
	// every standby logs it when its upstream disconnects, and treating it
	// as recycled WAL reseeded (and wiped) the whole chain during a rolling
	// deploy. A reused standby that hangs in recovery is caught by
	// standbyReuseStartupTimeout instead. "invalid resource manager ID" and
	// "invalid record length" are likewise not matched: they are the LOG
	// lines replay prints at the end of local WAL before streaming resumes,
	// and matching them reseeded a healthy standby on every restart.
	// The upstream lacks a history file this datadir needs to follow its
	// timeline; postgres retries forever without replicating.
	if strings.Contains(s, "could not receive timeline history file") {
		return true
	}
	return false
}

// ensureTimelineHistory writes missing ancestor timeline history files using
// the newest one, which lists every ancestor in order. pg_basebackup copies
// only the current timeline's history, and a standby restarting on an older
// timeline fetches every history file between its timeline and ours.
func ensureTimelineHistory(walDir string) error {
	entries, err := os.ReadDir(walDir)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	have := make(map[uint64]bool)
	var newest uint64
	for _, e := range entries {
		name := e.Name()
		if len(name) != 16 || !strings.HasSuffix(name, ".history") {
			continue
		}
		tli, err := strconv.ParseUint(name[:8], 16, 32)
		if err != nil {
			continue
		}
		have[tli] = true
		if tli > newest {
			newest = tli
		}
	}
	if newest == 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(walDir, fmt.Sprintf("%08X.history", newest)))
	if err != nil {
		return err
	}
	var prefix strings.Builder
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") {
			if tli, err := strconv.ParseUint(f[0], 10, 32); err == nil && tli > 1 && tli < newest && !have[tli] {
				path := filepath.Join(walDir, fmt.Sprintf("%08X.history", tli))
				if err := ioutil.WriteFile(path+".tmp", []byte(prefix.String()), 0600); err != nil {
					return err
				}
				if err := os.Rename(path+".tmp", path); err != nil {
					return err
				}
				have[tli] = true
			}
		}
		prefix.WriteString(line)
	}
	return nil
}

// verifyStandbyReuseResult decides whether a reused data directory must be
// thrown away. A WAL-recycle FATAL, a dead postmaster, or a health check that
// never saw stable streaming all reseed. A query error while postgres is still
// running is returned to the caller unless it is itself a WAL-recycle error.
func verifyStandbyReuseResult(running, walRecycled, healthy bool, healthErr error) (reseed bool, err error) {
	if walRecycled || !running {
		return true, nil
	}
	if healthErr != nil {
		if postgresWALSegmentRemoved(healthErr.Error()) {
			return true, nil
		}
		return false, healthErr
	}
	if !healthy {
		return true, nil
	}
	return false, nil
}

// startErrorNeedsReseed is true when postgres died during start() on a reused
// data directory because the upstream had already recycled the WAL the replica
// asked for. Returning that start error used to leave the replica down.
func startErrorNeedsReseed(reused, walRecycled, needsReseed bool) bool {
	return reused && (walRecycled || needsReseed)
}

// walRecycleDetector watches postgres stderr for a recycled-WAL FATAL so
// verifyOrReseedStandby and the unexpected-exit path can reseed instead of
// crash-looping on a datadir whose restart LSN is gone.
type walRecycleDetector struct {
	p   *Process
	mu  sync.Mutex
	buf []byte
}

func (w *walRecycleDetector) noteWALRecycle() {
	if w == nil || w.p == nil {
		return
	}
	w.p.setWALRecycled(true)
	w.p.setNeedsReseed(true)
	w.p.scheduleReseedAfterWALRecycle()
}

func (w *walRecycleDetector) Write(b []byte) (int, error) {
	if w == nil || w.p == nil {
		return len(b), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			if postgresWALSegmentRemoved(string(w.buf)) {
				w.noteWALRecycle()
			}
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if postgresWALSegmentRemoved(line) {
			w.noteWALRecycle()
		}
	}
	return len(b), nil
}

func (p *Process) dataDirInitialized() bool {
	if _, err := os.Stat(p.dataPath("PG_VERSION")); err != nil {
		return false
	}
	data, err := ioutil.ReadFile(p.sireniaIDMarkerPath())
	if err != nil {
		return false
	}
	return string(data) == p.id
}

func (p *Process) writeSireniaIDMarker() error {
	return ioutil.WriteFile(p.sireniaIDMarkerPath(), []byte(p.id), 0644)
}

func (p *Process) clearDataDir() error {
	entries, err := ioutil.ReadDir(p.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(p.dataDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (p *Process) basebackupStagingDir() string {
	return filepath.Join(filepath.Dir(p.dataDir), "."+filepath.Base(p.dataDir)+".basebackup")
}

// mkBasebackupStagingDir creates staging with the data directory's owner so
// pg_basebackup (which accepts an existing empty directory) does not need
// write access to the data directory's parent.
func (p *Process) mkBasebackupStagingDir(staging string) error {
	fi, err := os.Stat(p.dataDir)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Mkdir(staging, 0700); err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		return os.Chown(staging, int(st.Uid), int(st.Gid))
	}
	return nil
}

// replaceDataDir swaps the contents of staging into the data directory and
// removes staging. The data directory itself is kept so its mode and owner
// do not change.
func (p *Process) replaceDataDir(staging string) error {
	if err := p.clearDataDir(); err != nil {
		return err
	}
	if err := os.MkdirAll(p.dataDir, 0700); err != nil {
		return err
	}
	entries, err := ioutil.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(staging, e.Name()), filepath.Join(p.dataDir, e.Name())); err != nil {
			return err
		}
	}
	return os.RemoveAll(staging)
}

func (p *Process) sireniaIDMarkerPath() string {
	return p.dataPath(sireniaIDMarker)
}

// upstreamTimeout bounds how long a standby waits for its upstream to accept
// connections before failing pg_basebackup. Host restarts and sirenia failovers
// can leave the primary read-only for longer than one discoverd heartbeat.
var upstreamTimeout = 90 * time.Second

func (p *Process) waitForUpstream(upstream *discoverd.Instance) error {
	log := p.log.New("fn", "waitForUpstream", "upstream", upstream.Addr)
	log.Info("waiting for upstream to come online")
	upstreamClient := client.NewClient(upstream.Addr)

	stop := make(chan struct{})
	var stopOnce sync.Once
	p.upstreamWaitMtx.Lock()
	p.cancelUpstreamWait = func() { stopOnce.Do(func() { close(stop) }) }
	p.upstreamWaitMtx.Unlock()
	defer func() {
		p.upstreamWaitMtx.Lock()
		p.cancelUpstreamWait = func() {}
		p.upstreamWaitMtx.Unlock()
	}()

	type statusResult struct {
		status *client.Status
		err    error
	}
	timeout := time.After(upstreamTimeout)
	for {
		resCh := make(chan statusResult, 1)
		go func() {
			s, err := upstreamClient.Status()
			resCh <- statusResult{s, err}
		}()
		var res statusResult
		select {
		case res = <-resCh:
		case <-stop:
			log.Info("waitForUpstream canceled")
			return errors.New("upstream wait canceled")
		}
		status, err := res.status, res.err
		if err != nil {
			log.Error("error getting upstream status", "err", err)
		} else if status.Database != nil && status.Database.Running && status.Database.XLog != "" && status.Database.UserExists {
			log.Info("upstream is online")
			return nil
		}
		select {
		case <-stop:
			log.Info("waitForUpstream canceled")
			return errors.New("upstream wait canceled")
		case <-timeout:
			log.Error("upstream did not come online in time")
			return errors.New("upstream is offline")
		case <-time.After(checkInterval):
		}
	}
}

// CancelUpstreamWait aborts an in-progress waitForUpstream.
func (p *Process) CancelUpstreamWait() {
	p.upstreamWaitMtx.Lock()
	cancel := p.cancelUpstreamWait
	p.upstreamWaitMtx.Unlock()
	cancel()
}

func (p *Process) updateSync(downstream *discoverd.Instance) error {
	if downstream == nil {
		log := p.log.New("fn", "updateSync")
		log.Info("no downstream, remaining one-node-write")
		if err := p.writeConfig(configData{ReadOnly: false}); err != nil {
			log.Error("error writing postgres.conf", "path", p.configPath(), "err", err)
			return err
		}
		if err := p.sighup(); err != nil {
			p.log.Error("error reloading daemon configuration", "err", err)
			return err
		}
		return nil
	}
	log := p.log.New("fn", "updateSync", "downstream", downstream.Addr)
	log.Info("changing sync")

	config := configData{
		ReadOnly: true,
		Sync:     downstream.Meta[IDKey],
	}

	if err := p.writeConfig(config); err != nil {
		log.Error("error writing postgres.conf", "path", p.configPath(), "err", err)
		return err
	}

	if err := p.sighup(); err != nil {
		p.log.Error("error reloading daemon configuration", "err", err)
		return err
	}

	p.waitForSync(downstream, true)

	return nil
}

func (p *Process) start() error {
	log := p.log.New("fn", "start", "data_dir", p.dataDir, "bin_dir", p.binDir)
	log.Info("starting postgres")
	// A fresh basebackup must be allowed opTimeout. Capturing this before the
	// defer so a failed start of that backup is not treated as a reused hang.
	reusedHang := p.reusedStandbyStartup()
	defer func() { p.justBasebackup = false }()
	p.inStart.Store(true)
	defer p.inStart.Store(false)

	// clear stale pid if it exists
	os.Remove(filepath.Join(p.dataDir, "postmaster.pid"))

	p.expectExit.Store(false)
	p.daemonExit = make(chan struct{})
	p.setWALRecycled(false)
	p.setNeedsReseed(false)

	if err := ensureTimelineHistory(p.dataPath("pg_wal")); err != nil {
		log.Error("error restoring ancestor timeline history files", "err", err)
	}

	cmd := exec.Command(p.binPath("postgres"), "-D", p.dataDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &walRecycleDetector{p: p})
	if err := cmd.Start(); err != nil {
		log.Error("failed to start postgres", "err", err)
		return err
	}
	p.daemon = cmd
	p.setRunning(true)

	go func() {
		err := cmd.Wait()
		unexpected := !p.expectExit.Load().(bool)
		if unexpected {
			if p.walRecycled() {
				log.Error("postgres exited unexpectedly: requested WAL segment already removed", "err", err)
				p.setNeedsReseed(true)
			} else {
				log.Error("postgres exited unexpectedly", "err", err)
			}
			p.setRunning(false)
			p.dbMtx.Lock()
			if p.db != nil {
				p.db.Close()
				p.db = nil
			}
			p.dbMtx.Unlock()
		}
		close(p.daemonExit)
		if unexpected && p.needsReseed() {
			go p.reseedAfterWALRecycle()
		}
	}()

	log.Debug("waiting for postgres to start")
	timeout := time.After(p.standbyStartupWait())
	var err error
	for {
		if err = p.connectDB(); err == nil {
			log.Info("postgres started")
			return nil
		}

		log.Debug("ignoring error connecting to postgres", "err", err)
		if p.stopped() {
			// /stop sets this flag before it takes p.mtx. Abort immediately
			// so a deploy is not stuck behind a 5m "starting up" wait.
			log.Info("postgres start canceled by stop")
			p.killDaemon()
			return errors.New("stopped")
		}
		if p.walRecycled() {
			log.Error("postgres startup requested recycled WAL, aborting so we can reseed")
			if stopErr := p.stop(); stopErr != nil {
				log.Error("error stopping postgres after WAL recycle", "err", stopErr)
			}
			p.setNeedsReseed(true)
			return errors.New("postgres startup requested WAL that has already been removed")
		}
		select {
		case <-timeout:
			log.Error("timed out waiting for postgres to start", "err", err)
			if stopErr := p.stop(); stopErr != nil {
				log.Error("error stopping postgres", "err", stopErr)
			}
			if startupTimeoutNeedsReseed(reusedHang, p.walRecycled()) {
				// postgres.6770 on dev-node1 after node2's reboot: five-minute
				// "the database system is starting up" loops, never a reseed,
				// so the primary's sync never appeared in pg_stat_replication.
				p.setNeedsReseed(true)
				p.setWALRecycled(true)
				return errors.New("postgres startup hung on a reused standby")
			}
			return err
		case <-p.daemonExit:
			log.Error("postgres exited while waiting to accept connections", "err", err)
			p.setRunning(false)
			if err == nil {
				err = errors.New("postgres exited unexpectedly")
			}
			return err
		case <-time.After(checkInterval):
		}
	}
}

// dbAcquireTimeout bounds how long a caller waits for the single local pool
// connection, so a /status request can never hold dbMtx.RLock forever.
const dbAcquireTimeout = 10 * time.Second

// newLocalPool connects to the local postgres and returns a pool only once it
// has answered a query. pgx.NewConnPool returns a non-nil, zero-connection
// pool on a failed connect; concurrent Acquire calls on such a pool can wait
// forever because a failed reconnect never wakes the other waiters.
func newLocalPool(port string) (*pgx.ConnPool, error) {
	portNum, _ := strconv.Atoi(port)
	pool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig: pgx.ConnConfig{
			Host: "127.0.0.1",
			User: "postgres",
			Port: uint16(portNum),
		},
		MaxConnections: 1,
		AcquireTimeout: dbAcquireTimeout,
	})
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		return nil, err
	}
	if _, err := pool.Exec("SELECT 1"); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// connectDB publishes a working pool into p.db. p.db is left untouched on
// failure so readers never see a broken pool.
func (p *Process) connectDB() error {
	pool, err := newLocalPool(p.port)
	if err != nil {
		return err
	}
	p.dbMtx.Lock()
	old := p.db
	p.db = pool
	p.dbMtx.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

// reusedStandbyStartup reports whether this start is replaying an existing
// sync/async datadir. A just-finished basebackup is not a reuse: it needs
// the full opTimeout to reach "postgres started".
func (p *Process) reusedStandbyStartup() bool {
	if p.justBasebackup {
		return false
	}
	cfg := p.config()
	if cfg == nil || (cfg.Role != state.RoleSync && cfg.Role != state.RoleAsync) {
		return false
	}
	return p.dataDirInitialized()
}

// standbyStartupWait is how long start() waits for postgres to accept
// connections. A reused standby with recycled WAL hangs in crash recovery
// ("waiting for WAL to become available") well past a healthy boot.
func (p *Process) standbyStartupWait() time.Duration {
	if p.reusedStandbyStartup() {
		return standbyReuseStartupTimeout
	}
	return p.opTimeout
}

// startupTimeoutNeedsReseed is true when a reused standby's start budget
// expired. That is the "database system is starting up" hang after a node
// reboot, not a fresh basebackup that is still recovering.
func startupTimeoutNeedsReseed(reusedStandby, walRecycled bool) bool {
	return reusedStandby || walRecycled
}

// killDaemon SIGKILLs postgres without the graceful shutdown ladder. Used when
// /stop arrives during start(); the hung recovery does not answer SIGINT and
// the deploy client only waits 10s for /stop.
func (p *Process) killDaemon() {
	p.expectExit.Store(true)
	if p.daemon != nil && p.daemon.Process != nil {
		_ = p.daemon.Process.Signal(syscall.SIGKILL)
	}
	if p.daemonExit != nil {
		select {
		case <-p.daemonExit:
		case <-time.After(5 * time.Second):
		}
	}
	p.dbMtx.Lock()
	if p.db != nil {
		p.db.Close()
		p.db = nil
	}
	p.dbMtx.Unlock()
	p.setRunning(false)
}

func (p *Process) stop() error {
	log := p.log.New("fn", "stop")
	log.Info("stopping postgres")

	p.cancelSyncWait()
	p.dbMtx.Lock()
	if p.db != nil {
		p.db.Close()
		p.db = nil
	}
	p.dbMtx.Unlock()
	p.expectExit.Store(true)

	tryExit := func(sig os.Signal) bool {
		log.Debug("signalling daemon", "sig", sig)
		if p.daemon == nil || p.daemon.Process == nil {
			return true
		}
		if err := p.daemon.Process.Signal(sig); err != nil {
			log.Error("error signalling daemon", "sig", sig, "err", err)
		}
		select {
		case <-time.After(p.stopTimeout):
			return false
		case <-p.daemonExit:
			p.setRunning(false)
			return true
		}
	}

	// Forcefully disconnect all clients and shut down cleanly
	if tryExit(syscall.SIGINT) {
		return nil
	}

	// Quit immediately, will result in recovery at next start
	if tryExit(syscall.SIGQUIT) {
		return nil
	}

	// If all else fails, forcibly kill the process
	if tryExit(syscall.SIGKILL) {
		return nil
	}

	return errors.New("unable to kill postgres")
}

func (p *Process) sighup() error {
	p.log.Debug("reloading daemon configuration", "fn", "sighup")
	return p.daemon.Process.Signal(syscall.SIGHUP)
}

func (p *Process) waitForSync(inst *discoverd.Instance, enableWrites bool) {
	if inst == nil {
		return
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	var cancelOnce sync.Once
	p.cancelSyncWait = func() {
		cancelOnce.Do(func() {
			close(stopCh)
			<-doneCh
		})
	}
	go func() {
		defer close(doneCh)

		startTime := time.Now().UTC()
		appearanceTimeout := waitForSyncAppearanceTimeout(enableWrites, p.replTimeout)
		appearanceDeadline := startTime.Add(appearanceTimeout)
		replTimeout := time.NewTimer(p.replTimeout)
		defer replTimeout.Stop()
		seenRepl := false
		replState := ""
		var lastProgressLog time.Time
		lastFlushed := p.XLog().Zero()
		log := p.log.New(
			"fn", "waitForSync",
			"sync_name", inst.Meta[IDKey],
			"start_time", log15.Lazy{func() time.Time { return startTime }},
			"last_flushed", log15.Lazy{func() xlog.Position { return lastFlushed }},
		)

		shouldStop := func() bool {
			select {
			case <-stopCh:
				log.Debug("canceled, stopping")
				return true
			default:
				return false
			}
		}

		sleep := func() bool {
			select {
			case <-stopCh:
				log.Debug("canceled, stopping")
				return false
			case <-time.After(checkInterval):
				return true
			}
		}

		log.Info("waiting for downstream replication to catch up")

		writesEnabled := false
		enableWritesNow := func() {
			if !enableWrites || writesEnabled {
				return
			}
			if err := p.enablePrimaryWrites(log); err != nil {
				log.Error("error enabling writes after sync timeout", "err", err)
				return
			}
			writesEnabled = true
		}

		for {
			if shouldStop() {
				return
			}

			if seenRepl {
				select {
				case <-replTimeout.C:
					log.Error("error checking replication status", "err", "downstream unable to make forward progress")
					enableWritesNow()
					// Keep watching: after a rolling reboot the replica often
					// starts streaming just after this timer. Returning here
					// leaves SyncedDownstream unset and status.postgres down.
				default:
				}
			}

			sent, flushed, err := p.checkReplStatus(inst.Meta[IDKey])
			if err != nil && err.Error() != replState {
				replState = err.Error()
				if err == ErrNoReplicationStatus {
					log.Debug("downstream not in pg_stat_replication yet")
				} else {
					log.Error("error checking replication status", "err", err)
				}
			} else if err == nil && replState != "streaming" {
				replState = "streaming"
				log.Info("downstream replication streaming", "sent", sent, "flushed", flushed)
			}
			if err != nil {
				// Primary (enableWrites): a replica in discoverd that never
				// appears in pg_stat_replication (WAL recycled after a crash)
				// must still count toward the 1m timer so we enable writes.
				// Sync waiting on a new async: pg_basebackup uses the
				// upstream's application_name, so the replica is invisible
				// until streaming starts; wait the appearance budget first.
				if !seenRepl {
					if time.Now().UTC().After(appearanceDeadline) {
						log.Error("error checking replication status", "err", "downstream never appeared in pg_stat_replication")
						enableWritesNow()
					}
					if !sleep() {
						return
					}
					continue
				}
				if waitForSyncResetDeadline(false, false) {
					startTime = time.Now().UTC()
					replTimeout.Reset(p.replTimeout)
				}
				if !sleep() {
					return
				}
				continue
			}
			if !seenRepl {
				seenRepl = true
				startTime = time.Now().UTC()
				replTimeout.Reset(p.replTimeout)
			}
			log := log.New("sent", sent, "flushed", flushed, "elapsed", time.Now().Sub(startTime))

			if cmp, err := p.XLog().Compare(lastFlushed, flushed); err != nil {
				log.Error("error parsing log locations", "err", err)
				return
			} else if lastFlushed == p.XLog().Zero() || cmp == -1 {
				if waitForSyncResetDeadline(true, true) {
					log.Debug("flushed row incremented, resetting timeout")
					startTime = time.Now().UTC()
					replTimeout.Reset(p.replTimeout)
				}
				lastFlushed = flushed
			}

			if sent == flushed {
				log.Info("downstream caught up")
				p.setSyncedDownstream(inst)
				break
			}

			if time.Since(lastProgressLog) >= replProgressLogInterval {
				lastProgressLog = time.Now()
				log.Debug("continuing replication check")
			}
			if !sleep() {
				return
			}
		}

		if enableWrites {
			// sync caught up, enable write transactions
			if err := p.writeConfig(configData{Sync: inst.Meta[IDKey]}); err != nil {
				log.Error("error writing postgres.conf", "err", err)
				return
			}

			if err := p.sighup(); err != nil {
				log.Error("error calling sighup", "err", err)
				return
			}
		}
	}()
}

// waitForSyncResetDeadline reports whether the catch-up timer should restart.
// Missing pg_stat_replication must not reset it: after a crash reload the
// replica can stay in discoverd while WAL has already been recycled, so
// last_flushed never advances and resetting would leave the primary ReadOnly.
func waitForSyncResetDeadline(hasReplStatus, flushedAdvanced bool) bool {
	return hasReplStatus && flushedAdvanced
}

// waitForSyncAppearanceTimeout is how long waitForSync may wait for the
// first pg_stat_replication row. A primary must fail fast (replTimeout) so
// crash-reboot WAL-recycle cannot leave it ReadOnly. A sync waiting on a
// new async must cover pg_basebackup, which does not show the replica's
// application_name until streaming starts.
func waitForSyncAppearanceTimeout(enableWrites bool, replTimeout time.Duration) time.Duration {
	if enableWrites {
		return replTimeout
	}
	const backup = 15 * time.Minute
	if replTimeout > backup {
		return replTimeout
	}
	return backup
}

func (p *Process) enablePrimaryWrites(log log15.Logger) error {
	log.Warn("enabling primary writes after sync wait timeout")
	if err := p.writeConfig(configData{ReadOnly: false}); err != nil {
		return err
	}
	return p.sighup()
}

var ErrNoReplicationStatus = errors.New("no replication status")

// replProgressLogInterval limits how often waitForSync logs catch-up progress.
const replProgressLogInterval = 30 * time.Second

// checkReplStatus does not log: waitForSync calls it every checkInterval and
// logs only when the result changes.
func (p *Process) checkReplStatus(name string) (sent, flushed xlog.Position, err error) {
	var s, f pgx.NullString
	err = p.db.QueryRow(`
SELECT sent_lsn, flush_lsn
FROM pg_stat_replication
WHERE application_name = $1`, name).Scan(&s, &f)
	if err != nil && err != pgx.ErrNoRows {
		return
	}
	sent, flushed = xlog.Position(s.String), xlog.Position(f.String)
	if err == pgx.ErrNoRows || sent == "" || flushed == "" {
		err = ErrNoReplicationStatus
		return
	}
	return
}

// initDB initializes the postgres data directory for a new primary.
// If PG_VERSION already exists (volume reuse after an upgrade or restart),
// initdb is skipped so we do not spend seconds failing against a live cluster.
//
// This method should only be called by the primary of a shard. Standbys will
// not need to initialize, as they will restore from an already running primary.
func (p *Process) initDB() error {
	log := p.log.New("fn", "initDB", "dir", p.dataDir)
	log.Debug("starting initDB")

	if p.clusterAlreadyInitialized() {
		log.Info("data directory already initialized, skipping initdb")
		return p.writeHBAConf()
	}

	if err := p.runCmd(exec.Command(
		p.binPath("initdb"),
		"--pgdata", p.dataDir,
		"--username=postgres",
		"--encoding=UTF-8",
		"--locale=en_US.UTF-8",
	)); err != nil {
		log.Error("error initializing data directory", "err", err)
		return err
	}

	return p.writeHBAConf()
}

func (p *Process) clusterAlreadyInitialized() bool {
	_, err := os.Stat(p.dataPath("PG_VERSION"))
	return err == nil
}
func (p *Process) runCmd(cmd *exec.Cmd) error {
	p.log.Debug("running command", "fn", "runCmd", "cmd", cmd.Args)
	cmd.Stdout = os.Stdout
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// pg_basebackup and initdb run while p.mtx is held. /stop sets stopped
	// before it takes the lock; kill the child so Stop can proceed instead of
	// waiting out a basebackup the deploy client will abandon after 10s.
	tick := time.NewTicker(checkInterval)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-tick.C:
			if p.stopped() && cmd.Process != nil {
				_ = cmd.Process.Kill()
				err := <-done
				if err == nil {
					err = errors.New("stopped")
				}
				return err
			}
		}
	}
}

func (p *Process) writeConfig(d configData) error {
	d.ID = p.id
	d.Port = p.port
	d.TimescaleDB = p.timescaleDB
	d.ExtWhitelist = p.extWhitelist
	d.SHMType = p.shmType
	if err := p.ensureServerTLS(); err != nil {
		if p.log != nil {
			p.log.Error("error ensuring postgres TLS certs", "err", err)
		}
		return err
	}
	if p.sslEnabled() {
		d.SSL = true
		d.SSLCertFile = tlsCertFile
		d.SSLKeyFile = tlsKeyFile
		if fileExists(p.tlsCAPath()) {
			d.SSLCAFile = tlsCAFile
		}
	}
	f, err := os.Create(p.configPath())
	if err != nil {
		return err
	}
	defer f.Close()
	return configTemplate.Execute(f, d)
}

// createStandbySignal creates the standby.signal file required by PostgreSQL 12+
// to indicate that the server should start as a standby replica.
func (p *Process) createStandbySignal() error {
	f, err := os.Create(p.standbySignalPath())
	if err != nil {
		return err
	}
	return f.Close()
}

// removeStandbySignal removes the standby.signal file when promoting to primary
func (p *Process) removeStandbySignal() error {
	err := os.Remove(p.standbySignalPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// promote promotes a standby to primary using pg_ctl promote
// This is the PostgreSQL 16+ way to promote since promote_trigger_file was removed
func (p *Process) promote() error {
	log := p.log.New("fn", "promote")
	log.Info("promoting standby to primary using pg_ctl")
	return p.runCmd(exec.Command(
		p.binPath("pg_ctl"),
		"promote",
		"-D", p.dataDir,
	))
}

func (p *Process) writeHBAConf() error {
	return ioutil.WriteFile(p.hbaConfPath(), hbaConf, 0644)
}

func (p *Process) configPath() string {
	return p.dataPath("postgresql.conf")
}

func (p *Process) standbySignalPath() string {
	return p.dataPath("standby.signal")
}

func (p *Process) hbaConfPath() string {
	return p.dataPath("pg_hba.conf")
}

func (p *Process) triggerPath() string {
	return p.dataPath("promote.trigger")
}

func (p *Process) binPath(file string) string {
	return filepath.Join(p.binDir, file)
}

func (p *Process) dataPath(file string) string {
	return filepath.Join(p.dataDir, file)
}

type configData struct {
	ID       string
	Port     string
	Sync     string
	ReadOnly bool

	// Standby replication settings (PostgreSQL 12+)
	PrimaryConnInfo        string
	RecoveryTargetTimeline string

	TimescaleDB  bool
	ExtWhitelist bool
	SHMType      string

	SSL         bool
	SSLCertFile string
	SSLKeyFile  string
	SSLCAFile   string
}

var configTemplate = template.Must(template.New("postgresql.conf").Parse(`
unix_socket_directories = ''
listen_addresses = '0.0.0.0'
port = {{.Port}}
{{if .SSL}}ssl = on
ssl_cert_file = '{{.SSLCertFile}}'
ssl_key_file = '{{.SSLKeyFile}}'
{{if .SSLCAFile}}ssl_ca_file = '{{.SSLCAFile}}'{{end}}
{{else}}ssl = off
{{end}}
max_connections = 400
password_encryption = md5
shared_buffers = 32MB
wal_level = logical
fsync = on
max_wal_senders = 15
wal_keep_size = 4096MB
max_wal_size = 256MB
checkpoint_timeout = 30s
checkpoint_completion_target = 0.5
synchronous_commit = remote_write
{{if .Sync}}synchronous_standby_names = '"{{.Sync}}"'{{else}}synchronous_standby_names = ''{{end}}
{{if .ReadOnly}}
default_transaction_read_only = on
{{end}}
hot_standby = on
max_standby_archive_delay = 30s
max_standby_streaming_delay = 30s
wal_receiver_status_interval = 10s
hot_standby_feedback = on
log_destination = 'stderr'
logging_collector = false
log_line_prefix = '{{.ID}} %m '
log_timezone = 'UTC'
log_min_messages = 'LOG'
log_connections = on
log_disconnections = on
datestyle = 'iso, mdy'
timezone = 'UTC'
client_encoding = 'UTF8'
default_text_search_config = 'pg_catalog.english'

{{if .TimescaleDB}}
shared_preload_libraries = 'timescaledb'
# Flynn writes postgresql.conf itself; do not rely on timescaledb-tune (Debian's
# cluster conf is unused). Keep workers modest for appliance memory limits.
timescaledb.max_background_workers = 8
max_worker_processes = 16
max_parallel_workers = 4
{{end}}

{{if .SHMType}}
dynamic_shared_memory_type = '{{.SHMType}}'
{{end}}

{{if .ExtWhitelist}}
local_preload_libraries = 'pgextwlist'
extwlist.extensions = 'btree_gin,btree_gist,chkpass,citext,cube,dblink,dict_int,earthdistance,fuzzystrmatch,hstore,intarray,isn,ltree,pg_prewarm,pg_stat_statements,pg_trgm,pgcrypto,pgrouting,pgrowlocks,pgstattuple,plpgsql,plv8,postgis,postgis_topology,postgres_fdw,tablefunc,timescaledb,unaccent,uuid-ossp'
{{end}}

{{if .PrimaryConnInfo}}
# Standby replication settings (PostgreSQL 12+)
primary_conninfo = '{{.PrimaryConnInfo}}'
recovery_target_timeline = '{{.RecoveryTargetTimeline}}'
{{end}}
`[1:]))

var hbaConf = []byte(`
# TYPE  DATABASE        USER            ADDRESS                 METHOD
host    all             postgres        127.0.0.1/32            trust
host    all             all             127.0.0.1/32            md5
host    all             all             all                     md5
host    replication     flynn           all                     md5
`[1:])
