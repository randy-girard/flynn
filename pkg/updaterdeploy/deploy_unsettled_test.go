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
		{errors.New("timed out waiting for new instance to come up"), true},
		{errors.New("timed out waiting for new sirenia peer to come up"), true},
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

func TestShouldRetryAfterControllerUnavailable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("unknown_error: Something went wrong"), true},
		{errors.New("dial tcp 127.0.0.1:443: connection refused"), true},
		{errors.New("read: connection reset by peer"), true},
		{errors.New("i/o timeout"), true},
		{errors.New("release not found"), false},
		{errors.New("validation error"), false},
	}
	for _, tc := range cases {
		if got := ShouldRetryAfterControllerUnavailable(tc.err); got != tc.want {
			t.Fatalf("ShouldRetryAfterControllerUnavailable(%v)=%v want %v", tc.err, got, tc.want)
		}
	}
	sirenia := errors.New("timed out waiting for new sirenia peer to come up")
	if !ShouldRetryTransientSystemDeploy(sirenia) {
		t.Fatal("sirenia timeout must still retry")
	}
	if !ShouldRetryTransientSystemDeploy(errors.New("unknown_error: Something went wrong")) {
		t.Fatal("controller unknown_error after a postgres outage must retry")
	}
}

func TestTransientDeployRetryBudget(t *testing.T) {
	if MaxTransientDeployUnsettledAttempts() < 10 {
		t.Fatalf("retry budget %d is too low for post-upgrade settle", MaxTransientDeployUnsettledAttempts())
	}
	if TransientDeployRetryDelay() < 5*time.Second {
		t.Fatalf("retry delay %s is too short", TransientDeployRetryDelay())
	}
	instanceWait := errors.New("timed out waiting for new instance to come up")
	if !ShouldRetryTransientSystemDeploy(instanceWait) {
		t.Fatal("HA sirenia new-instance timeout must retry")
	}
	if got := MaxTransientDeployAttempts(instanceWait); got != MaxScaleTimeoutDeployAttempts() {
		t.Fatalf("new-instance timeout attempts=%d want %d", got, MaxScaleTimeoutDeployAttempts())
	}
	peerWait := errors.New("timed out waiting for new sirenia peer to come up")
	if got := MaxTransientDeployAttempts(peerWait); got != MaxScaleTimeoutDeployAttempts() {
		t.Fatalf("singleton sirenia peer timeout attempts=%d want %d", got, MaxScaleTimeoutDeployAttempts())
	}
	if MaxScaleTimeoutDeployAttempts() < 2 || MaxScaleTimeoutDeployAttempts() > 5 {
		t.Fatalf("scale-timeout retry budget %d should be a few attempts", MaxScaleTimeoutDeployAttempts())
	}
	nxdomain := errors.New("dial tcp: lookup leader.postgres.discoverd: no such host")
	if got := MaxTransientDeployAttempts(nxdomain); got != MaxTransientDeployUnsettledAttempts() {
		t.Fatalf("discoverd settle attempts=%d want %d", got, MaxTransientDeployUnsettledAttempts())
	}
}
