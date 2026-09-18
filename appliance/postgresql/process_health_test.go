package postgresql

import (
	"errors"
	"testing"

	"github.com/randy-girard/flynn/pkg/sirenia/client"
)

func TestSkipStandbyHealthWhenUpstreamDown(t *testing.T) {
	running := &client.Status{Database: &client.DatabaseInfo{Running: true}}
	stopped := &client.Status{Database: &client.DatabaseInfo{Running: false}}
	empty := &client.Status{}

	tests := []struct {
		name   string
		status *client.Status
		err    error
		want   bool
	}{
		{"dial error", running, errors.New("connection refused"), true},
		{"nil status", nil, nil, true},
		{"nil database", empty, nil, true},
		{"database not running", stopped, nil, true},
		{"database running", running, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := skipStandbyHealthWhenUpstreamDown(tc.status, tc.err)
			if got != tc.want {
				t.Fatalf("skipStandbyHealthWhenUpstreamDown() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPostgresStandbyLooksHealthy(t *testing.T) {
	tests := []struct {
		name     string
		receiver string
		start    string
		current  string
		want     bool
	}{
		{"streaming", "streaming", "0/1", "0/1", true},
		{"streaming even if LSN stuck", "streaming", "0/1", "0/1", true},
		{"advancing LSN", "", "0/100", "0/200", true},
		{"stuck receiver and LSN", "", "0/100", "0/100", false},
		{"empty start then same current is not yet advancing", "", "", "0/100", false},
		{"empty both", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := postgresStandbyLooksHealthy(tc.receiver, tc.start, tc.current)
			if got != tc.want {
				t.Fatalf("postgresStandbyLooksHealthy(%q, %q, %q) = %v, want %v",
					tc.receiver, tc.start, tc.current, got, tc.want)
			}
		})
	}
}
