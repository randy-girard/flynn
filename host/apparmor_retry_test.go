package main

import (
	"errors"
	"testing"
	"time"
)

func TestIsAppArmorApplyErr(t *testing.T) {
	if isAppArmorApplyErr(nil) {
		t.Fatal("nil")
	}
	if !isAppArmorApplyErr(errors.New("apply apparmor profile: write /proc/self/attr/exec: permission denied")) {
		t.Fatal("expected EPERM apply apparmor to match")
	}
	if !isAppArmorApplyErr(errors.New("apparmor failed to apply profile: permission denied")) {
		t.Fatal("expected apparmor failed to match")
	}
	if isAppArmorApplyErr(errors.New("file exists")) {
		t.Fatal("veth EEXIST is not AppArmor")
	}
}

func TestNextConfinedStartDelay(t *testing.T) {
	if _, ok := nextConfinedStartDelay(0, confinedStartAttempts); ok {
		t.Fatal("failureCount 0 should not retry")
	}
	d, ok := nextConfinedStartDelay(1, confinedStartAttempts)
	if !ok || d != 250*time.Millisecond {
		t.Fatalf("first retry delay=%s ok=%v", d, ok)
	}
	d, ok = nextConfinedStartDelay(2, confinedStartAttempts)
	if !ok || d != time.Second {
		t.Fatalf("second retry delay=%s ok=%v", d, ok)
	}
	if _, ok := nextConfinedStartDelay(3, confinedStartAttempts); ok {
		t.Fatal("no retry after max attempts")
	}
}
