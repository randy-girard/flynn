package client

import (
	"errors"
	"strings"
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func TestFormatReplicationProgress(t *testing.T) {
	if got := FormatReplicationProgress(nil, errors.New("connection refused")); !strings.Contains(got, "unreachable") {
		t.Fatalf("got %q", got)
	}
	if got := FormatReplicationProgress(nil, nil); !strings.Contains(got, "postgres not ready") {
		t.Fatalf("got %q", got)
	}
	backup := &Status{Peer: &state.PeerInfo{Role: state.RoleAsync}, Database: &DatabaseInfo{Running: false, XLog: ""}}
	if got := FormatReplicationProgress(backup, nil); !strings.Contains(got, "pg_basebackup") {
		t.Fatalf("expected base backup hint, got %q", got)
	}
	streaming := &Status{
		Peer: &state.PeerInfo{Role: state.RoleAsync},
		Database: &DatabaseInfo{
			Running: true,
			XLog:    "5/100",
			Config:  &state.Config{Upstream: &discoverd.Instance{Addr: "10.0.0.2:5432"}},
		},
	}
	up := &Status{
		Peer:     &state.PeerInfo{Role: state.RoleSync},
		Database: &DatabaseInfo{Running: true, XLog: "5/200", Config: &state.Config{Downstream: &discoverd.Instance{Addr: "10.0.0.3:5432"}}},
	}
	got := FormatReplSyncProgress(up, streaming, nil, nil)
	if !strings.Contains(got, "streaming WAL") || !strings.Contains(got, "replicating-from=10.0.0.2:5432") {
		t.Fatalf("expected streaming catch-up, got %q", got)
	}
	streaming.Database.XLog = "5/200"
	got = FormatReplSyncProgress(up, streaming, nil, nil)
	if !strings.Contains(got, "WAL caught up") {
		t.Fatalf("expected caught up, got %q", got)
	}
}
