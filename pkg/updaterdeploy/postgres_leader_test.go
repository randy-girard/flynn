package updaterdeploy

import (
	"errors"
	"testing"
)

func TestSireniaApplianceServices(t *testing.T) {
	want := []string{"postgres", "mariadb", "mongodb"}
	if len(SireniaApplianceServices) != len(want) {
		t.Fatalf("got %v want %v", SireniaApplianceServices, want)
	}
	for i, svc := range want {
		if SireniaApplianceServices[i] != svc {
			t.Fatalf("SireniaApplianceServices[%d] = %q want %q", i, SireniaApplianceServices[i], svc)
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
