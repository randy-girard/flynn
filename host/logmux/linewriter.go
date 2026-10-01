package logmux

import (
	"bytes"
	"strings"
)

// LineWriter splits TTY/PTY bytes on CR or LF and emits complete lines.
// Interactive echo often ends a command with CR before LF; both count as a line.
type LineWriter struct {
	WriteLine func(string)
	buf       []byte
}

func (w *LineWriter) Write(p []byte) (int, error) {
	if w == nil || w.WriteLine == nil {
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		skip := 1
		if w.buf[i] == '\r' && i+1 < len(w.buf) && w.buf[i+1] == '\n' {
			skip = 2
		}
		w.buf = w.buf[i+skip:]
		line = strings.TrimRight(line, "\r")
		if line != "" {
			w.WriteLine(line)
		}
	}
	const maxPending = 16 << 10
	if len(w.buf) > maxPending {
		w.WriteLine(string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}

func (w *LineWriter) Flush() {
	if w == nil || w.WriteLine == nil {
		return
	}
	line := strings.TrimRight(string(w.buf), "\r")
	w.buf = nil
	if line != "" {
		w.WriteLine(line)
	}
}
