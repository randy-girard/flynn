package types

import (
	"fmt"
	"time"
)

// BuildLock is the lease one push or deploy holds on an app while it builds
// (EDGE-09). gitreceive takes it before it downloads the app's repo cache and
// releases it after the upload; flynn-receiver takes it itself when nothing
// above it did (a GitHub deploy through taffy, a preview push). A second
// holder is refused until the lease is released or expires.
type BuildLock struct {
	AppID       string     `json:"app,omitempty"`
	Holder      string     `json:"holder,omitempty"`
	Description string     `json:"description,omitempty"`
	AcquiredAt  *time.Time `json:"acquired_at,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	// TTLSeconds is how long the lease lasts from this request. The holder
	// renews it by sending the same Holder again before it runs out.
	TTLSeconds int `json:"ttl_seconds,omitempty"`
}

// MaxBuildLockTTL caps one request's lease. A holder that stops renewing
// (its process died) frees the app within this time.
const MaxBuildLockTTL = 10 * time.Minute

// BuildLockHeldError is returned when another holder has the app's lease.
type BuildLockHeldError struct {
	Lock *BuildLock
}

func (e *BuildLockHeldError) Error() string {
	if e == nil || e.Lock == nil {
		return "another push to this app is in progress"
	}
	desc := e.Lock.Description
	if desc == "" {
		desc = "another push"
	}
	if e.Lock.AcquiredAt != nil {
		return fmt.Sprintf("%s to this app is in progress (started %s)", desc, e.Lock.AcquiredAt.UTC().Format("15:04:05 UTC"))
	}
	return desc + " to this app is in progress"
}
