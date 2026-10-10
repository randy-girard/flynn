package postgres

import (
	"errors"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx"
)

func TestIsTransientLeaderDialErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`dial tcp: lookup leader.postgres.discoverd on 100.100.58.1:53: no such host`), true},
		{errors.New("connection refused"), true},
		{pgx.ErrDeadConn, true},
		{io.EOF, true},
		{errors.New(`ERROR: syntax error`), false},
	}
	for _, tc := range cases {
		got := IsTransientLeaderDialErr(tc.err)
		if got != tc.want {
			t.Fatalf("transient(%v): got %v want %v", tc.err, got, tc.want)
		}
	}
}

// A primary whose VM reboots never sends a RST. Pooled connections must give
// up within leaderDeadPeerTimeout or they exhaust the controller's pool.
func TestLeaderDialerDetectsDeadPeer(t *testing.T) {
	d := leaderDialer()
	ka := d.KeepAliveConfig
	if !ka.Enable {
		t.Fatal("keepalive must be enabled")
	}
	if detect := ka.Idle + ka.Interval*time.Duration(ka.Count); detect > leaderDeadPeerTimeout {
		t.Fatalf("keepalive detects a dead peer after %s, want <= %s", detect, leaderDeadPeerTimeout)
	}
	if runtime.GOOS == "linux" && d.Control == nil {
		t.Fatal("linux dialer must set TCP_USER_TIMEOUT for in-flight queries")
	}
}
