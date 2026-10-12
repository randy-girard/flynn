package postgresql

import (
	"bytes"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/randy-girard/flynn/pkg/sirenia/client"
)

// pg_basebackup --progress writes "1234/56789 kB (2%), 0/1 tablespace",
// optionally followed by the current file name with --verbose.
var basebackupProgressRe = regexp.MustCompile(`(\d+)/(\d+) kB \((\d+)%\)`)

// parseBasebackupProgress returns the report in line, if any.
func parseBasebackupProgress(line []byte) (*client.BasebackupProgress, bool) {
	m := basebackupProgressRe.FindSubmatch(line)
	if m == nil {
		return nil, false
	}
	done, err1 := strconv.ParseInt(string(m[1]), 10, 64)
	total, err2 := strconv.ParseInt(string(m[2]), 10, 64)
	pct, err3 := strconv.Atoi(string(m[3]))
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, false
	}
	if pct > 100 {
		pct = 100
	}
	return &client.BasebackupProgress{
		DoneBytes:  done * 1024,
		TotalBytes: total * 1024,
		Percent:    pct,
		UpdatedAt:  time.Now().UTC(),
	}, true
}

// basebackupProgressWriter parses pg_basebackup stderr and stores the latest
// report. Reports end in "\r" on a terminal and "\n" otherwise.
type basebackupProgressWriter struct {
	dst *atomic.Pointer[client.BasebackupProgress]
	buf []byte
}

func (w *basebackupProgressWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		if pr, ok := parseBasebackupProgress(w.buf[:i]); ok {
			w.dst.Store(pr)
		}
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > 4096 {
		w.buf = w.buf[len(w.buf)-4096:]
	}
	return len(b), nil
}
