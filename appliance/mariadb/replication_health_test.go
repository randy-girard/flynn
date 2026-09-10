package mariadb

import (
	"testing"

	"github.com/flynn/flynn/pkg/sirenia/state"
	"github.com/flynn/flynn/pkg/sirenia/xlog"
)

func TestReplicationCaughtUpWithUpstream(t *testing.T) {
	p := NewProcess()

	tests := []struct {
		name     string
		local    xlog.Position
		upstream xlog.Position
		want     bool
	}{
		{"empty local", "", "0-1-10", false},
		{"empty upstream", "0-1-10", "", false},
		{"behind upstream", "0-1684291852-74", "0-1684291859-76", false},
		{"caught up", "0-1684291859-76", "0-1684291859-76", true},
		{"ahead of upstream", "0-1684291859-77", "0-1684291859-76", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.replicationCaughtUpWithUpstream(tc.local, tc.upstream)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("replicationCaughtUpWithUpstream(%q, %q) = %v, want %v", tc.local, tc.upstream, got, tc.want)
			}
		})
	}
}

func TestClassifyStandbyReplication(t *testing.T) {
	p := NewProcess()
	cmp := p.XLog().Compare

	tests := []struct {
		name         string
		startPos     xlog.Position
		endPos       xlog.Position
		upstreamPos  xlog.Position
		ioRunning    bool
		sqlRunning   bool
		lastIOErrno  int64
		lastSQLErrno int64
		want         standbyReplState
	}{
		{
			name:        "caught up",
			startPos:    "0-1-10",
			endPos:      "0-1-20",
			upstreamPos: "0-1-20",
			ioRunning:   true,
			sqlRunning:  true,
			want:        standbyReplHealthy,
		},
		{
			name:        "behind but advancing",
			startPos:    "0-1-10",
			endPos:      "0-1-15",
			upstreamPos: "0-1-20",
			ioRunning:   true,
			sqlRunning:  true,
			want:        standbyReplHealthy,
		},
		{
			name:        "behind and not advancing",
			startPos:    "0-1-10",
			endPos:      "0-1-10",
			upstreamPos: "0-1-20",
			ioRunning:   true,
			sqlRunning:  true,
			want:        standbyReplStuck,
		},
		{
			name:       "io stopped",
			startPos:   "0-1-10",
			endPos:     "0-1-10",
			ioRunning:  false,
			sqlRunning: true,
			want:       standbyReplStuck,
		},
		{
			name:         "sql stopped with 1062",
			startPos:     "0-1-10",
			endPos:       "0-1-10",
			ioRunning:    true,
			sqlRunning:   false,
			lastSQLErrno: 1062,
			want:         standbyReplStuck,
		},
		{
			name:        "fatal 1236",
			startPos:    "0-1-10",
			endPos:      "0-1-10",
			ioRunning:   false,
			sqlRunning:  false,
			lastIOErrno: 1236,
			want:        standbyReplFatal,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStandbyReplication(tc.startPos, tc.endPos, tc.upstreamPos, tc.ioRunning, tc.sqlRunning, tc.lastIOErrno, tc.lastSQLErrno, cmp)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestReusedStandbyUnreachablePolicy(t *testing.T) {
	if got := reusedStandbyUnreachablePolicy(state.RoleSync); got != reusedStandbySkipCheck {
		t.Fatalf("sync unreachable must skip health check for takeover, got %v", got)
	}
	if got := reusedStandbyUnreachablePolicy(state.RoleAsync); got != reusedStandbyDeferCheck {
		t.Fatalf("async unreachable must defer reseed, got %v", got)
	}
	if got := reusedStandbyUnreachablePolicy(state.RolePrimary); got != reusedStandbyDeferCheck {
		t.Fatalf("non-sync roles defer, got %v", got)
	}
}
