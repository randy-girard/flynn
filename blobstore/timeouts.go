package main

import (
	"net/http"
	"time"

	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// EDGE-33: a client that stops sending a PUT body is cut off instead of
// keeping its request (and, before the upload rework, a database transaction
// and row lock) open for as long as the TCP connection lives.
const (
	// defaultUploadIdleTimeout is how long a PUT body may send nothing.
	// It is generous: a build that pauses between writes, or a slow link,
	// keeps going. Operators change it with `flynn -a blobstore env:set
	// UPLOAD_IDLE_TIMEOUT=<duration>`; 0 or off turns it off.
	defaultUploadIdleTimeout = uploadlimit.DefaultIdleTimeout

	// abandonedUploadAge is how long an upload may make no progress before
	// the sweep deletes its object. It is far longer than the idle timeout,
	// so only uploads whose blobstore instance stopped, or whose idle
	// timeout is off, are swept.
	abandonedUploadAge = time.Hour

	// sweepInterval is how often the server sweeps abandoned uploads.
	sweepInterval = 10 * time.Minute
)

// uploadIdleTimeoutFromEnv reads UPLOAD_IDLE_TIMEOUT.
func uploadIdleTimeoutFromEnv() (time.Duration, error) {
	return uploadlimit.DurationFromEnv("UPLOAD_IDLE_TIMEOUT", defaultUploadIdleTimeout)
}

// parseIdleTimeout reads a duration ("90s", "2m") or a number of seconds.
// An empty s is def; "0" and "off" are 0 (no timeout).
func parseIdleTimeout(s string, def time.Duration) (time.Duration, error) {
	return uploadlimit.ParseDuration(s, def)
}

// applyServerTimeouts sets the server-wide limits shared with tarreceive and
// gitreceive (uploadlimit.ApplyServerTimeouts).
func applyServerTimeouts(s *http.Server) {
	uploadlimit.ApplyServerTimeouts(s)
}

// wrapHandler is the middleware every blobstore request goes through.
func wrapHandler(h http.Handler) http.Handler {
	return httphelper.ContextInjector("blobstore", httphelper.NewRequestLogger(h))
}

// errBodyIdle is returned by a body that saw no data for its timeout.
var errBodyIdle = uploadlimit.ErrIdle
