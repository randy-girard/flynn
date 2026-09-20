package main

import (
	"strings"
	"time"
)

// confinedStartAttempts is the number of confined (AppArmor profile applied)
// container starts to try before falling back to fail-closed (user jobs) or
// unconfined retry (system/build jobs).
const confinedStartAttempts = 3

var confinedStartSleep = time.Sleep

var confinedStartBackoffs = []time.Duration{
	250 * time.Millisecond,
	time.Second,
}

func isAppArmorApplyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "apparmor") || strings.Contains(msg, "apply apparmor")
}

// nextConfinedStartDelay returns the sleep before confined start retry
// `failureCount` (1 after the first failure). ok is false when no retries
// remain (failureCount has already reached maxAttempts).
func nextConfinedStartDelay(failureCount, maxAttempts int) (time.Duration, bool) {
	if failureCount <= 0 || failureCount >= maxAttempts {
		return 0, false
	}
	idx := failureCount - 1
	if idx >= len(confinedStartBackoffs) {
		return confinedStartBackoffs[len(confinedStartBackoffs)-1], true
	}
	return confinedStartBackoffs[idx], true
}
