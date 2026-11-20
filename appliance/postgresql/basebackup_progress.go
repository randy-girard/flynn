package postgresql

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// pg_basebackup --progress writes "1234/56789 kB (2%), 0/1 tablespace",
// optionally followed by the current file name with --verbose.
var basebackupProgressRe = regexp.MustCompile(`(\d+)/(\d+) kB \((\d+)%\)`)

// parseBasebackupProgress returns the report in line, if any.
func parseBasebackupProgress(line []byte) (*client.BasebackupProgress, bool) {
	m := basebackupProgressRe.FindSubmatch(line)
	if m == nil {
		return nil, false
	}
	done, err1 := strconv.ParseInt(string(m[1]), 10, 64)
	total, err2 := strconv.ParseInt(string(m[2]), 10, 64)
	pct, err3 := strconv.Atoi(string(m[3]))
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, false
	}
	if pct > 100 {
		pct = 100
	}
	return &client.BasebackupProgress{
		DoneBytes:  done * 1024,
		TotalBytes: total * 1024,
		Percent:    pct,
		UpdatedAt:  time.Now().UTC(),
	}, true
}

// basebackupProgressWriter parses pg_basebackup stderr and stores the latest
// report. Reports end in "\r" on a terminal and "\n" otherwise.
type basebackupProgressWriter struct {
	dst *atomic.Pointer[client.BasebackupProgress]
	buf []byte
}

func (w *basebackupProgressWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		if pr, ok := parseBasebackupProgress(w.buf[:i]); ok {
			w.dst.Store(pr)
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 {
		w.buf = w.buf[len(w.buf)-4096:]
	}
	return len(b), nil
}

// basebackupConnTimeouts makes pg_basebackup notice an upstream whose host
// vanished without closing the connection (a node reboot). Without them the
// main connection waited on the system TCP timeouts while the reseed held
// p.mtx: 31 minutes on the dev cluster.
const basebackupConnTimeouts = "connect_timeout=10 keepalives=1 keepalives_idle=10 keepalives_interval=5 keepalives_count=3 tcp_user_timeout=30000"

// basebackupStallTimeout kills a pg_basebackup that has written nothing to
// stderr for this long before reporting 100%. It backs up the libpq
// timeouts. --progress reports at least once a second while data arrives;
// the quiet phases before the copy are a fast checkpoint and connecting.
var basebackupStallTimeout = 3 * time.Minute

// errBasebackupSuperseded aborts a pull from an upstream that sirenia has
// replaced.
var errBasebackupSuperseded = errors.New("basebackup canceled: upstream superseded by a new configuration")

// activityWriter records when a command last wrote output.
type activityWriter struct {
	last atomic.Int64
}

func (w *activityWriter) touch() { w.last.Store(time.Now().UnixNano()) }

func (w *activityWriter) Write(b []byte) (int, error) {
	w.touch()
	return len(b), nil
}

func (w *activityWriter) idle() time.Duration {
	return time.Since(time.Unix(0, w.last.Load()))
}

// basebackupSuperseded reports that next no longer streams from pulling: the
// peer was promoted or deposed, or its upstream is another instance or the
// same instance at a new address. A standby config without an upstream is
// invalid and rejected by Reconfigure, so it does not cancel anything.
func basebackupSuperseded(pulling *discoverd.Instance, next *state.Config) bool {
	if pulling == nil || next == nil {
		return false
	}
	switch next.Role {
	case state.RoleSync, state.RoleAsync:
		if next.Upstream == nil {
			return false
		}
		return next.Upstream.Addr != pulling.Addr || next.Upstream.Meta[IDKey] != pulling.Meta[IDKey]
	default:
		return true
	}
}

func (p *Process) setBasebackupUpstream(upstream *discoverd.Instance, superseded chan struct{}) {
	p.basebackupMtx.Lock()
	defer p.basebackupMtx.Unlock()
	p.basebackupUpstream = upstream
	if superseded == nil {
		p.cancelBasebackup = nil
		return
	}
	var once sync.Once
	p.cancelBasebackup = func() { once.Do(func() { close(superseded) }) }
}

// cancelSupersededBasebackup aborts an in-flight pg_basebackup whose upstream
// next replaces.
func (p *Process) cancelSupersededBasebackup(next *state.Config) {
	p.basebackupMtx.Lock()
	defer p.basebackupMtx.Unlock()
	if p.cancelBasebackup == nil || !basebackupSuperseded(p.basebackupUpstream, next) {
		return
	}
	p.log.Warn("canceling basebackup from superseded upstream", "fn", "cancelSupersededBasebackup", "upstream", p.basebackupUpstream.Addr, "role", next.Role)
	p.cancelBasebackup()
}
