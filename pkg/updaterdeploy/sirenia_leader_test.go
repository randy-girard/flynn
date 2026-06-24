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

func TestSkipOptionalSireniaLeaderWait(t *testing.T) {
	cases := []struct {
		service       string
		instanceCount int
		instancesErr  error
		want          bool
	}{
		{"postgres", 0, errors.New("not found"), false},
		{"postgres", 0, nil, false},
		{"mariadb", 0, nil, true},
		{"mariadb", 0, errors.New("service not found"), true},
		{"mariadb", 1, nil, false},
		{"mongodb", 0, nil, true},
		{"mongodb", 2, nil, false},
		{"redis", 0, nil, false},
	}
	for _, tc := range cases {
		got := skipOptionalSireniaLeaderWait(tc.service, tc.instanceCount, tc.instancesErr)
		if got != tc.want {
			t.Fatalf("skipOptionalSireniaLeaderWait(%q, %d, %v): got %v want %v",
				tc.service, tc.instanceCount, tc.instancesErr, got, tc.want)
		}
	}
}
