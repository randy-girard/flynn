package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/randy-girard/flynn/pkg/httphelper"
)

// EDGE-33: a client that stops sending a PUT body is cut off instead of
// keeping its request (and, before the upload rework, a database transaction
// and row lock) open for as long as the TCP connection lives.
const (
	// defaultUploadIdleTimeout is how long a PUT body may send nothing.
	// It is generous: a build that pauses between writes, or a slow link,
	// keeps going. Operators change it with `flynn -a blobstore env:set
	// UPLOAD_IDLE_TIMEOUT=<duration>`; 0 or off turns it off.
	defaultUploadIdleTimeout = 60 * time.Second

	// readHeaderTimeout bounds how long a client takes to send its request
	// line and headers.
	readHeaderTimeout = 30 * time.Second

	// idleConnTimeout closes keep-alive connections that carry no request.
	idleConnTimeout = 2 * time.Minute

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
	d, err := parseIdleTimeout(os.Getenv("UPLOAD_IDLE_TIMEOUT"), defaultUploadIdleTimeout)
	if err != nil {
		return 0, fmt.Errorf("error parsing UPLOAD_IDLE_TIMEOUT: %s", err)
	}
	return d, nil
}

// parseIdleTimeout reads a duration ("90s", "2m") or a number of seconds.
// An empty s is def; "0" and "off" are 0 (no timeout).
func parseIdleTimeout(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return def, nil
	case "0", "off", "none":
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		n, nerr := strconv.ParseInt(s, 10, 64)
		if nerr != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		d = time.Duration(n) * time.Second
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration %q: negative", s)
	}
	return d, nil
}

// applyServerTimeouts sets the server-wide limits. There is no ReadTimeout or
// WriteTimeout: a whole-request deadline would cut off large uploads and
// downloads that are still moving. Stalled PUT bodies are cut off by the
// idle timeout on each read instead.
func applyServerTimeouts(s *http.Server) {
	s.ReadHeaderTimeout = readHeaderTimeout
	s.IdleTimeout = idleConnTimeout
}

// wrapHandler is the middleware every blobstore request goes through.
func wrapHandler(h http.Handler) http.Handler {
	return httphelper.ContextInjector("blobstore", httphelper.NewRequestLogger(h))
}

// errBodyIdle is returned by an idleBody that saw no data for its timeout.
var errBodyIdle = errors.New("request body sent no data within the upload idle timeout")

// idleBody sets a read deadline on the connection before each read of the
// request body, so a body that stops arriving fails after timeout while one
// that keeps sending, however slowly, does not.
type idleBody struct {
	io.ReadCloser
	rc       *http.ResponseController
	timeout  time.Duration
	timedOut bool
	disabled bool
}

func newIdleBody(w http.ResponseWriter, body io.ReadCloser, timeout time.Duration) *idleBody {
	return &idleBody{ReadCloser: body, rc: http.NewResponseController(w), timeout: timeout}
}

func (b *idleBody) Read(p []byte) (int, error) {
	if !b.disabled {
		if err := b.rc.SetReadDeadline(time.Now().Add(b.timeout)); err != nil {
			// the connection does not support deadlines (HTTP/2 through a
			// proxy, a test recorder): read without one
			log.Printf("upload idle timeout not applied: %s", err)
			b.disabled = true
		}
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil && errors.Is(err, os.ErrDeadlineExceeded) {
		b.timedOut = true
		return n, errBodyIdle
	}
	return n, err
}

// done clears the deadline once the body has been read, so writing the
// response and the next request on the connection are not affected.
func (b *idleBody) done() {
	if !b.disabled {
		b.rc.SetReadDeadline(time.Time{})
	}
}
