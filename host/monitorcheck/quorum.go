package monitorcheck

import "time"

// HaveQuorum is whether remaining hosts can repair after a node failure.
// Waiting for the full expected host count would leave the cluster without
// a scheduler until the dead VM comes back (vagrant reload).
func HaveQuorum(have, want int) bool {
	if have < 1 {
		return false
	}
	if want < 1 {
		want = 1
	}
	return have >= want/2+1
}

// FaultDeadline is how long the cluster may stay unhealthy before repair.
// A missing host plus a down scheduler is a node crash: repair on remaining
// hosts without waiting a full minute.
func FaultDeadline(have, want int, schedulerDown bool, check, generic time.Duration) time.Duration {
	if want < 1 {
		want = 1
	}
	if schedulerDown && have < want {
		return check
	}
	return generic
}
