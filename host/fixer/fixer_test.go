package fixer

import "testing"

func TestJobIPOnSubnet(t *testing.T) {
	if !jobIPOnSubnet("100.100.83.5", "100.100.83.1/24") {
		t.Fatal("expected 83.5 on 83.x subnet")
	}
	if jobIPOnSubnet("100.100.38.5", "100.100.83.1/24") {
		t.Fatal("expected stale 38.x address to be rejected")
	}
}
