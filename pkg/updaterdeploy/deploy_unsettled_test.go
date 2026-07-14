package updaterdeploy

import (
	"errors"
	"fmt"
	"testing"
)

func TestShouldRetryAfterScaleTimeout(t *testing.T) {
	if !ShouldRetryAfterScaleTimeout(fmt.Errorf("timed out waiting for scale to complete (waited 120 seconds)")) {
		t.Fatal("expected scale timeout to be retryable")
	}
	if ShouldRetryAfterScaleTimeout(nil) {
		t.Fatal("nil should not retry")
	}
}

func TestShouldRetryAfterUnsettledDiscoverdLeader(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("sirenia: waiting for quorum"), true},
		{errors.New("dial tcp: lookup leader.postgres.discoverd: no such host"), true},
		{errors.New("postgres.discoverd: connection refused"), true},
		{errors.New("something went wrong"), false},
		{errors.New("leader.mongodb.discoverd: no such host"), true},
		{errors.New("generic no such host"), false},
	}
	for _, tc := range cases {
		got := ShouldRetryAfterUnsettledDiscoverdLeader(tc.err)
		if got != tc.want {
			t.Fatalf("retry(%q): got %v want %v", tc.err, got, tc.want)
		}
	}
}
