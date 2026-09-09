package mariadb

import (
	"testing"

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
