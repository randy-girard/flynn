package client

import (
	"fmt"
	"strings"
)

const zeroXLog = "0/00000000"

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
	upLine := FormatReplicationProgress(up, upErr)
	downLine := FormatReplicationProgress(down, downErr)
	detail := walCatchup(up, down)
	if detail != "" {
		return upLine + "; " + downLine + "; " + detail
	}
	return upLine + "; " + downLine
}

func walCatchup(up, down *Status) string {
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
	if ux == dx {
		return "WAL caught up"
	}
	return fmt.Sprintf("streaming WAL, catching up (upstream=%s replica=%s)", ux, dx)
}
