// Package uploadlimit bounds the size of request bodies that system
// components (blobstore, tarreceive, gitreceive) write to disk or storage.
//
// Limits come from the component's environment. A value is a byte count or a
// size with a binary unit ("512m", "32g", "1t"); "0" or "off" turns the limit
// off.
package uploadlimit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	units "github.com/docker/go-units"
)

// ErrTooLarge is returned by a Reader once the body passes its limit.
var ErrTooLarge = errors.New("upload exceeds the size limit")

// FromEnv reads the size limit named by key. An unset or empty value is def.
// A limit of 0 means no limit.
func FromEnv(key string, def int64) (int64, error) {
	return Parse(os.Getenv(key), def)
}

// Parse reads a size limit. An empty s is def; "0" and "off" are 0 (no
// limit).
func Parse(s string, def int64) (int64, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return def, nil
	case "0", "off", "none", "unlimited":
		return 0, nil
	}
	n, err := units.RAMInBytes(s)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %s", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("invalid size %q: negative", s)
	}
	return n, nil
}

// Format prints a limit for an error message ("32 GiB (34359738368 bytes)").
func Format(n int64) string {
	return fmt.Sprintf("%s (%d bytes)", units.BytesSize(float64(n)), n)
}

// Reader passes through at most Limit bytes of R. A read past the limit
// returns ErrTooLarge and sets Exceeded, so a caller whose error is wrapped
// or replaced further down can still tell the body was too large.
type Reader struct {
	R        io.Reader
	Limit    int64
	Exceeded bool
	limited  bool
	n        int64
}

// NewReader limits r to limit bytes. A limit of 0 or less is no limit,
// matching the settings, where 0 turns a limit off.
func NewReader(r io.Reader, limit int64) *Reader {
	return &Reader{R: r, Limit: limit, limited: limit > 0}
}

// NewBudgetReader limits r to max bytes, where max may be 0 (any byte is
// too many). A negative max is 0.
func NewBudgetReader(r io.Reader, max int64) *Reader {
	if max < 0 {
		max = 0
	}
	return &Reader{R: r, Limit: max, limited: true}
}

func (l *Reader) Read(p []byte) (int, error) {
	if l.Exceeded {
		return 0, ErrTooLarge
	}
	if !l.limited {
		n, err := l.R.Read(p)
		l.n += int64(n)
		return n, err
	}
	// read one byte past the limit so a body of exactly Limit bytes passes
	// and a longer one is caught without waiting for EOF
	if max := l.Limit - l.n + 1; int64(len(p)) > max {
		p = p[:max]
	}
	n, err := l.R.Read(p)
	l.n += int64(n)
	if l.n > l.Limit {
		l.Exceeded = true
		n -= int(l.n - l.Limit)
		l.n = l.Limit
		return n, ErrTooLarge
	}
	return n, err
}

// N is the number of bytes passed through so far.
func (l *Reader) N() int64 { return l.n }
