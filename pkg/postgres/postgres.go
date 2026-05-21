package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/attempt"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/jackc/pgx"
)

const (
	InvalidTextRepresentation = "22P02"
	CheckViolation            = "23514"
	UniqueViolation           = "23505"
	RaiseException            = "P0001"
	ForeignKeyViolation       = "23503"

	// postgresReconnectBudget caps how long we retry transient discoverd/DNS
	// failures before failing hard. Restarts normally settle sooner; callers
	// (process restart or clients) should retry rather than wedging minutes.
	postgresReconnectBudget = 90 * time.Second

	// leaderDialBudget allows longer waits inside each TCP dial than the initial
	// Open() retry loop: sirenia postgres updates can leave leader.* NXDOMAIN
	// for minutes while roles move.
	leaderDialBudget = 4 * time.Minute
)

type Conf struct {
	Discoverd *discoverd.Client
	Service   string
	User      string
	Password  string
	Database  string

	// SingletonCluster is set by Wait from discoverd service meta when the
	// cluster runs in Sirenia singleton mode (typical single-host Flynn). It is
	// informational; leader host selection uses discoverd whenever leader.* DNS
	// is unavailable (not only for singleton clusters) so the controller can
	// reach Postgres while discoverd DNS catches up.
	SingletonCluster bool
}

var connectAttempts = attempt.Strategy{
	Min:   5,
	Total: postgresReconnectBudget,
	Delay: 300 * time.Millisecond,
}

// listenAttempts retries acquiring a dedicated connection for LISTEN after the
// pool was opened (e.g. controller EventListener). Unlike postgres.Wait at
// startup, a plain ConnPool.Acquire fails immediately when discoverd DNS for
// leader.<service>.discoverd returns NXDOMAIN during sirenia/postgres restarts.
var listenAttempts = attempt.Strategy{
	Min:   5,
	Total: postgresReconnectBudget,
	Delay: 300 * time.Millisecond,
}

// leaderDialAttempts retries a single net.Dial while leader DNS or the primary
// socket is unsettled (common during postgres appliance updates).
var leaderDialAttempts = attempt.Strategy{
	Min:   10,
	Total: leaderDialBudget,
	Delay: 400 * time.Millisecond,
}

func isTransientLeaderDialErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	if err == pgx.ErrDeadConn {
		return true
	}
	msg := strings.ToLower(err.Error())
	if dnerr, ok := err.(*net.DNSError); ok && dnerr.IsTemporary {
		return true
	}
	if dnerr, ok := err.(*net.DNSError); ok && dnerr.IsNotFound {
		return true
	}
	return strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "temporary failure") ||
		strings.Contains(msg, "server misbehaving") ||
		strings.Contains(msg, "context deadline exceeded")
}

// IsTransientLeaderDialErr reports whether err is retryable connectivity while
// discoverd DNS or sirenia-managed postgres is restarting (dead connections,
// NXDOMAIN, abrupt EOF from the remote, etc.).
func IsTransientLeaderDialErr(err error) bool {
	return isTransientLeaderDialErr(err)
}

var leaderDNSWait = attempt.Strategy{
	Min:   5,
	Total: postgresReconnectBudget,
	Delay: 400 * time.Millisecond,
}

// waitUntilPostgresResolvable blocks until connections can be established using
// postgresLeaderHost: either leader.<service>.discoverd resolves, or (singleton
// cluster only) discoverd reports a TCP leader instance with a literal IP so
// we are not stuck on NXDOMAIN on single-node setups where DNS trails HTTP state.
func waitUntilPostgresResolvable(conf *Conf) {
	fqdn := fmt.Sprintf("leader.%s.discoverd", conf.Service)
	_ = leaderDNSWait.Run(func() error {
		if _, err := net.LookupHost(fqdn); err == nil {
			return nil
		}
		if discoverdLeaderIPKnown(conf) {
			return nil
		}
		return fmt.Errorf("postgres leader not yet resolvable (DNS or discoverd)")
	})
}

func discoverdLeaderIPKnown(conf *Conf) bool {
	inst, err := conf.Discoverd.Service(conf.Service).Leader()
	if err != nil || inst == nil || inst.Addr == "" {
		return false
	}
	return net.ParseIP(inst.Host()) != nil
}

// postgresLeaderHost chooses the TCP host for pgx. Prefer the leader FQDN
// when discoverd DNS resolves it; otherwise use the discoverd-reported primary
// address (IP) so we can connect while leader.* DNS is missing or stale.
// retryingLeaderDial calls this on every dial attempt, so failover updates
// from discoverd are picked up without pinning an old address in the pool.
func postgresLeaderHost(conf *Conf) string {
	fqdn := fmt.Sprintf("leader.%s.discoverd", conf.Service)
	if _, err := net.LookupHost(fqdn); err == nil {
		return fqdn
	}
	if inst, err := conf.Discoverd.Service(conf.Service).Leader(); err == nil && inst != nil {
		if h := inst.Host(); net.ParseIP(h) != nil {
			return h
		}
	}
	return fqdn
}

// retryingLeaderDial returns a pgx.DialFunc that re-resolves the sirenia leader
// on every attempt (FQDN or singleton discoverd IP) and retries transient DNS /
// TCP failures. The address pgx passes through is only used for the port.
func retryingLeaderDial(conf *Conf) pgx.DialFunc {
	d := &net.Dialer{KeepAlive: 5 * time.Minute}
	return func(network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var conn net.Conn
		dialErr := leaderDialAttempts.RunWithValidator(func() error {
			host := postgresLeaderHost(conf)
			tcpAddr := net.JoinHostPort(host, port)
			c, err := d.Dial(network, tcpAddr)
			if err != nil {
				return err
			}
			conn = c
			return nil
		}, isTransientLeaderDialErr)
		if dialErr != nil {
			return nil, dialErr
		}
		return conn, nil
	}
}

func New(connPool *pgx.ConnPool, conf *Conf) *DB {
	return &DB{connPool, conf, ""}
}

func Wait(conf *Conf, afterConn func(*pgx.Conn) error) *DB {
	if conf == nil {
		conf = &Conf{
			Service:  os.Getenv("FLYNN_POSTGRES"),
			User:     os.Getenv("PGUSER"),
			Password: os.Getenv("PGPASSWORD"),
			Database: os.Getenv("PGDATABASE"),
		}
	}
	if conf.Discoverd == nil {
		conf.Discoverd = discoverd.DefaultClient
	}

	// Retry watching the discoverd service to handle transient unavailability
	// during updates (e.g. when discoverd containers are being replaced and
	// DNS resolution temporarily fails).
	var watchAttempts = attempt.Strategy{
		Total: 2 * time.Minute,
		Delay: time.Second,
	}
	var watchStream interface{ Close() error }
	var events chan *discoverd.Event
	err := watchAttempts.Run(func() error {
		events = make(chan *discoverd.Event)
		var err error
		watchStream, err = conf.Discoverd.Service(conf.Service).Watch(events)
		return err
	})
	if err != nil {
		shutdown.Fatal(err)
	}
	// wait for service meta that has sync or singleton primary
	for e := range events {
		if e.Kind&discoverd.EventKindServiceMeta == 0 || e.ServiceMeta == nil || len(e.ServiceMeta.Data) == 0 {
			continue
		}
		state := &state.State{}
		json.Unmarshal(e.ServiceMeta.Data, state)
		if state.Singleton || state.Sync != nil {
			conf.SingletonCluster = state.Singleton
			break
		}
	}
	watchStream.Close()
	// TODO(titanous): handle discoverd disconnection

	waitUntilPostgresResolvable(conf)

	// retry here as authentication may fail if DB is still
	// starting up.
	// TODO(jpg): switch this to use pgmanager to check if user
	// exists, we can also check for r/w with pgmanager
	var db *DB
	err = connectAttempts.Run(func() error {
		db, err = Open(conf, afterConn)
		return err
	})
	if err != nil {
		panic(err)
	}
	for {
		var readonly string
		// wait until read-write transactions are allowed
		if err := db.QueryRow("SHOW default_transaction_read_only").Scan(&readonly); err != nil || readonly == "on" {
			time.Sleep(100 * time.Millisecond)
			// TODO(titanous): add max wait here
			continue
		}
		return db
	}
}

func Open(conf *Conf, afterConn func(*pgx.Conn) error) (*DB, error) {
	host := postgresLeaderHost(conf)
	connConfig := pgx.ConnConfig{
		Host:     host,
		User:     conf.User,
		Database: conf.Database,
		Password: conf.Password,
		Dial:     retryingLeaderDial(conf),
	}
	connPool, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig:     connConfig,
		AfterConnect:   afterConn,
		MaxConnections: 20,
		AcquireTimeout: 30 * time.Second,
	})
	db := &DB{connPool, conf, host}
	return db, err
}

type DB struct {
	*pgx.ConnPool
	conf     *Conf
	dialHost string // TCP host baked into ConnPool ConnConfig.Host at Open()
}

// DialHost returns the pg host this pool opens connections to (set at Open).
func (db *DB) DialHost() string {
	if db == nil {
		return ""
	}
	return db.dialHost
}

func (db *DB) Exec(query string, args ...interface{}) error {
	_, err := db.ConnPool.Exec(query, args...)
	return err
}

func (db *DB) ExecRetry(query string, args ...interface{}) error {
	retries := 0
	max := 30
	for {
		_, err := db.ConnPool.Exec(query, args...)
		if err == pgx.ErrDeadConn && retries < max {
			retries++
			time.Sleep(1 * time.Second)
			continue
		}
		return err
	}
}

type Scanner interface {
	Scan(...interface{}) error
}

func (db *DB) QueryRow(query string, args ...interface{}) Scanner {
	return rowErrFixer{db.ConnPool.QueryRow(query, args...)}
}

func (db *DB) Begin() (*DBTx, error) {
	tx, err := db.ConnPool.Begin()
	return &DBTx{tx}, err
}

type DBTx struct{ *pgx.Tx }

func (tx *DBTx) Exec(query string, args ...interface{}) error {
	_, err := tx.Tx.Exec(query, args...)
	return err
}

func (tx *DBTx) QueryRow(query string, args ...interface{}) Scanner {
	return rowErrFixer{tx.Tx.QueryRow(query, args...)}
}

type rowErrFixer struct {
	s Scanner
}

func (f rowErrFixer) Scan(args ...interface{}) error {
	err := f.s.Scan(args...)
	if e, ok := err.(pgx.PgError); ok && e.Code == InvalidTextRepresentation && e.File == "uuid.c" && e.Routine == "string_to_uuid" {
		// invalid input syntax for uuid
		err = pgx.ErrNoRows
	}
	return err
}

func IsUniquenessError(err error, constraint string) bool {
	if e, ok := err.(pgx.PgError); ok && e.Code == UniqueViolation {
		return constraint == "" || constraint == e.ConstraintName
	}
	return false
}

func IsPostgresCode(err error, code string) bool {
	if e, ok := err.(pgx.PgError); ok && e.Code == code {
		return true
	}
	return false
}
