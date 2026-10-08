package monitorcheck

import (
	"testing"
	"time"
)

func TestHaveQuorum(t *testing.T) {
	cases := []struct {
		have, want int
		ok         bool
	}{
		{0, 3, false},
		{1, 3, false},
		{2, 3, true},
		{3, 3, true},
		{1, 1, true},
		{1, 2, false},
		{0, 1, false},
		{1, 0, true},
	}
	for _, tc := range cases {
		got := HaveQuorum(tc.have, tc.want)
		if got != tc.ok {
			t.Errorf("have=%d want=%d: got %v, want %v", tc.have, tc.want, got, tc.ok)
		}
	}
}

func TestFaultDeadlineNodeCrash(t *testing.T) {
	check := 10 * time.Second
	generic := 60 * time.Second
	if d := FaultDeadline(2, 3, true, check, generic); d != check {
		t.Fatalf("missing host + down scheduler: got %s, want %s", d, check)
	}
	if d := FaultDeadline(3, 3, true, check, generic); d != generic {
		t.Fatalf("full cluster + down scheduler: got %s, want %s", d, generic)
	}
	if d := FaultDeadline(2, 3, false, check, generic); d != generic {
		t.Fatalf("missing host + scheduler up: got %s, want %s", d, generic)
	}
	if d := FaultDeadline(3, 3, false, check, generic); d != generic {
		t.Fatalf("healthy cluster: got %s, want %s", d, generic)
	}
}
