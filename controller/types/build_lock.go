package types

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// BuildLock is the lease one push or deploy holds on an app while it builds
// (EDGE-09). gitreceive takes it before it downloads the app's repo cache and
// releases it after the upload; flynn-receiver takes it itself when nothing
// above it did (a GitHub deploy through taffy, a preview push). A second
// holder is refused until the lease is released or expires.
type BuildLock struct {
	AppID string `json:"app,omitempty"`
	// Key names a lease that is not one app's (EDGE-40), such as
	// "pipeline:shop" for the repo cache behind a pipeline's preview remote.
	Key         string     `json:"key,omitempty"`
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

// BuildLockPipelinePrefix starts the key of a pipeline's lease (EDGE-40):
// gitreceive holds "pipeline:<name>" around a push to that pipeline's preview
// remote, whose repo cache no app lease covers.
const BuildLockPipelinePrefix = "pipeline:"

// BuildLockKeyPattern is a keyed lease's name: a lowercase namespace, a
// colon and an id of at most 200 characters (EDGE-40).
var BuildLockKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}:[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

// PipelineBuildLockKey is the lease key for a pipeline's preview remote.
func PipelineBuildLockKey(pipeline string) string {
	return BuildLockPipelinePrefix + pipeline
}

// BuildLockHeldError is returned when another holder has the app's lease.
type BuildLockHeldError struct {
	Lock *BuildLock
}

func (e *BuildLockHeldError) Error() string {
	if e == nil || e.Lock == nil {
		return "another push to this app is in progress"
	}
	target := "this app"
	if strings.HasPrefix(e.Lock.Key, BuildLockPipelinePrefix) {
		target = "this pipeline"
	}
	desc := e.Lock.Description
	if desc == "" {
		desc = "another push"
	}
	if e.Lock.AcquiredAt != nil {
		return fmt.Sprintf("%s to %s is in progress (started %s)", desc, target, e.Lock.AcquiredAt.UTC().Format("15:04:05 UTC"))
	}
	return desc + " to " + target + " is in progress"
}
