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

func TestXLogBytesAndLag(t *testing.T) {
	if n, ok := XLogBytes("5/2B0F8EE0"); !ok || n != 5<<32|0x2B0F8EE0 {
		t.Fatalf("got %d %v", n, ok)
	}
	for _, bad := range []string{"", "5", "x/1", "1/zz"} {
		if _, ok := XLogBytes(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
	if lag, ok := LagBytes("5/2B227E08", "5/2B0F8EE0"); !ok || lag != 0x2B227E08-0x2B0F8EE0 {
		t.Fatalf("lag %d %v", lag, ok)
	}
	if lag, ok := LagBytes("5/100", "5/200"); !ok || lag != 0 {
		t.Fatalf("ahead replica lag %d %v", lag, ok)
	}
	if got := FormatBytes(11*1024*1024*1024 + 300*1024*1024); got != "11.3 GB" {
		t.Fatalf("got %q", got)
	}
	if got := FormatBytes(512); got != "512 B" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatReplicationProgressBasebackupPercent(t *testing.T) {
	st := &Status{
		Peer: &state.PeerInfo{Role: state.RoleAsync},
		Database: &DatabaseInfo{Basebackup: &BasebackupProgress{
			DoneBytes: 5 * 1024 * 1024 * 1024, TotalBytes: 10 * 1024 * 1024 * 1024, Percent: 50,
		}},
	}
	if got := FormatReplicationProgress(st, nil); got != "async: pg_basebackup 50% (5.0 GB of 10.0 GB)" {
		t.Fatalf("got %q", got)
	}
}

func TestCatchupTrackerPercent(t *testing.T) {
	var tr CatchupTracker
	up := &Status{Peer: &state.PeerInfo{Role: state.RoleSync}, Database: &DatabaseInfo{Running: true, XLog: "0/1000"}}
	down := &Status{Peer: &state.PeerInfo{ID: "r1", Role: state.RoleAsync}, Database: &DatabaseInfo{Running: true, XLog: "0/0C00"}}
	if got := tr.FormatReplSync(up, down, nil, nil); !strings.Contains(got, "catching up 0%, 1.0 KB behind") {
		t.Fatalf("first sample %q", got)
	}
	down.Database.XLog = "0/0F00"
	if got := tr.FormatReplSync(up, down, nil, nil); !strings.Contains(got, "catching up 75%, 256 B behind") {
		t.Fatalf("second sample %q", got)
	}
	down.Database.XLog = "0/1000"
	if got := tr.FormatReplSync(up, down, nil, nil); !strings.Contains(got, "WAL caught up") {
		t.Fatalf("caught up %q", got)
	}
	if tr.Percent("r1", 10) != 0 {
		t.Fatal("caught up should reset the largest lag")
	}
	if got := FormatReplSyncProgress(up, &Status{Database: &DatabaseInfo{Running: true, XLog: "0/0800"}}, nil, nil); !strings.Contains(got, "catching up, 2.0 KB behind") {
		t.Fatalf("untracked %q", got)
	}
}
