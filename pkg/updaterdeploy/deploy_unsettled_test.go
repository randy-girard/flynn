package updaterdeploy

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestShouldRetryAfterScaleTimeout(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("timed out waiting for scale to complete (waited 120 seconds)"), true},
		{errors.New("Timed Out Waiting For Scale To Complete"), true},
		{errors.New("deploy failed: timeout"), false},
	}
	for _, tc := range cases {
		if got := ShouldRetryAfterScaleTimeout(tc.err); got != tc.want {
			t.Fatalf("ShouldRetryAfterScaleTimeout(%v)=%v want %v", tc.err, got, tc.want)
		}
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
		{errors.New("leader.mariadb.discoverd: i/o timeout"), true},
		{errors.New("leader.mongodb.discoverd: no such host"), true},
		{errors.New("leader.maria.discoverd unavailable"), true},
		{errors.New("lookup foo.postgres.bar: no such host"), true},
		{errors.New("something went wrong"), false},
		{errors.New("generic no such host"), false},
	}
	for _, tc := range cases {
		got := ShouldRetryAfterUnsettledDiscoverdLeader(tc.err)
		if got != tc.want {
			t.Fatalf("retry(%q): got %v want %v", tc.err, got, tc.want)
		}
	}
}

func TestTransientDeployRetryBudget(t *testing.T) {
	if MaxTransientDeployUnsettledAttempts() < 10 {
		t.Fatalf("retry budget %d is too low for post-upgrade settle", MaxTransientDeployUnsettledAttempts())
	}
	if TransientDeployRetryDelay() < 5*time.Second {
		t.Fatalf("retry delay %s is too short", TransientDeployRetryDelay())
	}
}
