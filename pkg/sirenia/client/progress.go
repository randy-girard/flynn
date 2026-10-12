package client

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const zeroXLog = "0/00000000"

// XLogBytes converts an LSN such as "5/2B0F8EE0" to a byte offset.
func XLogBytes(pos string) (uint64, bool) {
	hi, lo, ok := strings.Cut(strings.TrimSpace(pos), "/")
	if !ok {
		return 0, false
	}
	h, err := strconv.ParseUint(hi, 16, 32)
	if err != nil {
		return 0, false
	}
	l, err := strconv.ParseUint(lo, 16, 32)
	if err != nil {
		return 0, false
	}
	return h<<32 | l, true
}

// LagBytes is how many WAL bytes down is behind up (0 when caught up or ahead).
func LagBytes(up, down string) (int64, bool) {
	u, ok := XLogBytes(up)
	if !ok {
		return 0, false
	}
	d, ok := XLogBytes(down)
	if !ok {
		return 0, false
	}
	if d >= u {
		return 0, true
	}
	return int64(u - d), true
}

// FormatBytes renders n in binary units ("11.3 GB").
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// FormatBasebackup is "pg_basebackup 45% (5.1 GB of 11.3 GB)".
func FormatBasebackup(b *BasebackupProgress) string {
	if b == nil {
		return "pg_basebackup"
	}
	if b.TotalBytes > 0 {
		return fmt.Sprintf("pg_basebackup %d%% (%s of %s)", b.Percent, FormatBytes(b.DoneBytes), FormatBytes(b.TotalBytes))
	}
	return fmt.Sprintf("pg_basebackup %d%%", b.Percent)
}

// CatchupTracker turns WAL lag into a catch-up percent by remembering the
// largest lag seen for each replica since it was last caught up.
type CatchupTracker struct {
	mu  sync.Mutex
	max map[string]int64
}

// Percent is 100 * (1 - lag / largest lag seen for key); 100 when caught up.
func (t *CatchupTracker) Percent(key string, lag int64) int {
	if t == nil {
		return -1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.max == nil {
		t.max = map[string]int64{}
	}
	if lag <= 0 {
		delete(t.max, key)
		return 100
	}
	if lag > t.max[key] {
		t.max[key] = lag
	}
	return int(100 * (t.max[key] - lag) / t.max[key])
}

// FormatReplicationProgress is a one-line snapshot of a sirenia peer for
// deploy logs: whether postgres is running, whether it is still in
// pg_basebackup, WAL position, upstream, and whether a downstream is synced.
func FormatReplicationProgress(status *Status, err error) string {
	if err != nil {
		return "unreachable: " + err.Error()
	}
	if status == nil || status.Database == nil {
		return "no database status (discoverd registered, postgres not ready)"
	}
	db := status.Database
	role := "peer"
	if status.Peer != nil && status.Peer.Role.String() != "" {
		role = status.Peer.Role.String()
	}
	if !db.Running {
		if db.Basebackup != nil {
			return fmt.Sprintf("%s: %s", role, FormatBasebackup(db.Basebackup))
		}
		if db.XLog == "" || db.XLog == zeroXLog {
			return fmt.Sprintf("%s: postgres not running, likely pg_basebackup (xlog=%q)", role, db.XLog)
		}
		return fmt.Sprintf("%s: postgres not running (xlog=%s)", role, db.XLog)
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%s running xlog=%s", role, db.XLog))
	if db.Config != nil && db.Config.Upstream != nil && db.Config.Upstream.Addr != "" {
		parts = append(parts, "replicating-from="+db.Config.Upstream.Addr)
	}
	if db.SyncedDownstream != nil && db.SyncedDownstream.Addr != "" {
		parts = append(parts, "synced-downstream="+db.SyncedDownstream.Addr)
	} else if db.Config != nil && db.Config.Downstream != nil && db.Config.Downstream.Addr != "" {
		parts = append(parts, "waiting-for-downstream="+db.Config.Downstream.Addr)
	}
	if db.ReadWrite {
		parts = append(parts, "read-write")
	}
	return strings.Join(parts, " ")
}

// FormatReplSyncProgress describes an upstream/replica pair during WaitForReplSync.
func FormatReplSyncProgress(up, down *Status, upErr, downErr error) string {
	return (*CatchupTracker)(nil).FormatReplSync(up, down, upErr, downErr)
}

// FormatReplSync is FormatReplSyncProgress plus a catch-up percent derived
// from the largest lag this tracker has seen for the replica.
func (t *CatchupTracker) FormatReplSync(up, down *Status, upErr, downErr error) string {
	upLine := FormatReplicationProgress(up, upErr)
	downLine := FormatReplicationProgress(down, downErr)
	detail := walCatchup(up, down, t)
	if detail != "" {
		return upLine + "; " + downLine + "; " + detail
	}
	return upLine + "; " + downLine
}

func walCatchup(up, down *Status, t *CatchupTracker) string {
	if up == nil || down == nil || up.Database == nil || down.Database == nil {
		return ""
	}
	if !up.Database.Running || !down.Database.Running {
		return ""
	}
	ux, dx := up.Database.XLog, down.Database.XLog
	if ux == "" || dx == "" || ux == zeroXLog || dx == zeroXLog {
		return "WAL streaming not established"
	}
	lag, ok := LagBytes(ux, dx)
	if ux == dx || (ok && lag == 0) {
		if t != nil {
			t.Percent(downKey(down), 0)
		}
		return "WAL caught up"
	}
	if !ok {
		return fmt.Sprintf("streaming WAL, catching up (upstream=%s replica=%s)", ux, dx)
	}
	if pct := t.Percent(downKey(down), lag); pct >= 0 {
		return fmt.Sprintf("streaming WAL, catching up %d%%, %s behind (upstream=%s replica=%s)", pct, FormatBytes(lag), ux, dx)
	}
	return fmt.Sprintf("streaming WAL, catching up, %s behind (upstream=%s replica=%s)", FormatBytes(lag), ux, dx)
}

func downKey(s *Status) string {
	if s != nil && s.Peer != nil && s.Peer.ID != "" {
		return s.Peer.ID
	}
	return "replica"
}
