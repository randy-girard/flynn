package uploadlimit

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Server and body timeouts shared by the services that take uploads
// (blobstore, tarreceive, gitreceive; EDGE-33, EDGE-36).
const (
	// DefaultIdleTimeout is how long an upload body may send nothing before
	// the request is cut off. It is generous: a client that pauses between
	// writes, or a slow link, keeps going.
	DefaultIdleTimeout = 60 * time.Second

	// ReadHeaderTimeout bounds how long a client takes to send its request
	// line and headers.
	ReadHeaderTimeout = 30 * time.Second

	// ConnIdleTimeout closes keep-alive connections that carry no request.
	ConnIdleTimeout = 2 * time.Minute
)

// ErrIdle is returned by an IdleBody that saw no data for its timeout.
var ErrIdle = errors.New("request body sent no data within the upload idle timeout")

// DurationFromEnv reads the duration setting named by key (see
// ParseDuration); the error names the setting.
func DurationFromEnv(key string, def time.Duration) (time.Duration, error) {
	d, err := ParseDuration(os.Getenv(key), def)
	if err != nil {
		return 0, fmt.Errorf("error parsing %s: %s", key, err)
	}
	return d, nil
}

// ParseDuration reads a duration ("90s", "2m") or a number of seconds. An
// empty s is def; "0", "off" and "none" are 0 (off).
func ParseDuration(s string, def time.Duration) (time.Duration, error) {
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

// ApplyServerTimeouts sets the server-wide limits. There is no ReadTimeout or
// WriteTimeout: a whole-request deadline would cut off large uploads and
// downloads that are still moving. Stalled bodies are cut off by an IdleBody
// instead.
func ApplyServerTimeouts(s *http.Server) {
	s.ReadHeaderTimeout = ReadHeaderTimeout
	s.IdleTimeout = ConnIdleTimeout
}

// IdleBody sets a read deadline on the connection before each read of a
// request body, so a body that stops arriving fails with ErrIdle after its
// timeout while one that keeps sending, however slowly, does not. The
// deadline is cleared when the body ends (EOF or an error) and by Done, so
// the work after the body (unpacking a layer, a git hook) and the response
// are not cut off: the server's background read would otherwise hit the
// deadline and cancel the request.
//
// The caller answers a TimedOut body with 408 and "Connection: close".
type IdleBody struct {
	io.ReadCloser
	rc       *http.ResponseController
	timeout  time.Duration
	timedOut bool
	// off: no deadline is set (timeout 0, no deadline support, or the
	// body has ended)
	off bool
}

// NewIdleBody wraps body, the request body of w. A timeout of 0 or less
// reads without a deadline.
func NewIdleBody(w http.ResponseWriter, body io.ReadCloser, timeout time.Duration) *IdleBody {
	return &IdleBody{ReadCloser: body, rc: http.NewResponseController(w), timeout: timeout, off: timeout <= 0}
}

func (b *IdleBody) Read(p []byte) (int, error) {
	if !b.off {
		if err := b.rc.SetReadDeadline(time.Now().Add(b.timeout)); err != nil {
			// the connection does not support deadlines (HTTP/2 through a
			// proxy, a test recorder): read without one
			b.off = true
		}
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			b.timedOut = true
			err = ErrIdle
		}
		b.Done()
	}
	return n, err
}

// Done clears the deadline. Call it once the body has been read, or will
// not be read any further.
func (b *IdleBody) Done() {
	if !b.off {
		b.rc.SetReadDeadline(time.Time{})
		b.off = true
	}
}

// TimedOut reports whether the body was cut off for sending nothing.
func (b *IdleBody) TimedOut() bool { return b.timedOut }

// Timeout is the idle timeout of b.
func (b *IdleBody) Timeout() time.Duration { return b.timeout }
